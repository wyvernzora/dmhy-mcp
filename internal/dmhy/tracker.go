package dmhy

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// TrackerProber probes BitTorrent tracker URLs for liveness and caches the
// result. Probes happen asynchronously: ShouldKeep returns immediately based
// on the cache, optimistically including unknown / expired entries while
// scheduling a background re-probe. Subsequent calls benefit from the warmed
// cache.
//
// The prober supports HTTP/HTTPS trackers (HEAD probe) and UDP trackers
// (BEP-15 connect probe). All other URL schemes are treated as dead.
type TrackerProber struct {
	mu       sync.RWMutex
	statuses map[string]trackerStatus

	queue  chan string
	stop   chan struct{}
	wg     sync.WaitGroup
	closed bool

	ttl     time.Duration
	timeout time.Duration

	httpClient *http.Client
	logger     *slog.Logger
}

type trackerStatus struct {
	alive   bool
	checked time.Time
}

// TrackerProberConfig configures a TrackerProber. Zero values fall back to
// reasonable defaults (10 workers, 1h TTL, 3s probe timeout).
type TrackerProberConfig struct {
	Workers int
	TTL     time.Duration
	Timeout time.Duration
	Logger  *slog.Logger
}

// NewTrackerProber builds and starts a prober. Stop with Close.
func NewTrackerProber(cfg TrackerProberConfig) *TrackerProber {
	if cfg.Workers <= 0 {
		cfg.Workers = 10
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 1 * time.Hour
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// Tracker servers commonly send a non-spec response body to HEAD
	// requests, which Go's HTTP client surfaces as an "Unsolicited response
	// received on idle HTTP channel" log line if the connection sits in the
	// keep-alive pool afterwards. Disabling keep-alives (and forcing each
	// probe onto a fresh connection that we close on response) silences that
	// noise without affecting probe correctness.
	transport := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: cfg.Timeout,
		DialContext: (&net.Dialer{
			Timeout: cfg.Timeout,
		}).DialContext,
		TLSHandshakeTimeout: cfg.Timeout,
	}
	p := &TrackerProber{
		statuses: make(map[string]trackerStatus),
		queue:    make(chan string, 4096),
		stop:     make(chan struct{}),
		ttl:      cfg.TTL,
		timeout:  cfg.Timeout,
		httpClient: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		logger: cfg.Logger,
	}
	for i := 0; i < cfg.Workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

// Close stops the background workers. Safe to call multiple times.
func (p *TrackerProber) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	close(p.stop)
	p.wg.Wait()
}

// ShouldKeep reports whether a tracker URL should be retained in a magnet.
// Cached-alive → true. Cached-dead → false. Unknown or expired → true,
// with an async probe queued so the next call sees an updated answer.
func (p *TrackerProber) ShouldKeep(rawURL string) bool {
	p.mu.RLock()
	s, ok := p.statuses[rawURL]
	expired := ok && time.Since(s.checked) > p.ttl
	p.mu.RUnlock()
	if !ok || expired {
		p.enqueue(rawURL)
		return true
	}
	return s.alive
}

// Prime queues a set of tracker URLs for background probing without affecting
// any current decision. Used to warm the cache from search results before
// the agent calls get_magnets.
func (p *TrackerProber) Prime(urls []string) {
	for _, u := range urls {
		p.mu.RLock()
		s, ok := p.statuses[u]
		expired := ok && time.Since(s.checked) > p.ttl
		p.mu.RUnlock()
		if !ok || expired {
			p.enqueue(u)
		}
	}
}

func (p *TrackerProber) enqueue(rawURL string) {
	select {
	case p.queue <- rawURL:
	default:
		// Queue full; drop. Re-probe will happen on next ShouldKeep miss.
	}
}

func (p *TrackerProber) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		case rawURL := <-p.queue:
			alive, reason := p.probeWithReason(rawURL)
			p.mu.Lock()
			prev, hadPrev := p.statuses[rawURL]
			p.statuses[rawURL] = trackerStatus{alive: alive, checked: time.Now()}
			p.mu.Unlock()
			// Only log on first observation or on alive↔dead transitions —
			// re-probes that confirm the existing state are silent to keep
			// logs useful.
			if !hadPrev || prev.alive != alive {
				p.logger.Debug("tracker probe",
					"tracker", rawURL,
					"alive", alive,
					"reason", reason,
				)
			}
		}
	}
}

// probeWithReason returns the alive flag and a short reason string suitable
// for structured logging. The reason names the failure mode (or "ok" on
// success) without including the full URL, which the caller adds separately.
func (p *TrackerProber) probeWithReason(rawURL string) (bool, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, "url_parse_error"
	}
	switch u.Scheme {
	case "http", "https":
		return p.probeHTTP(rawURL)
	case "udp":
		return p.probeUDP(u.Host)
	default:
		return false, "unsupported_scheme:" + u.Scheme
	}
}

func (p *TrackerProber) probeHTTP(rawURL string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, "request_build_error"
	}
	// Some trackers reject HEAD with a body anyway; closing the connection
	// on response is enforced by Transport.DisableKeepAlives (set in
	// NewTrackerProber). Connection: close also signals intent to the peer.
	req.Header.Set("Connection", "close")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false, classifyHTTPError(err)
	}
	defer resp.Body.Close()
	// Trackers often return 4xx for HEAD without proper announce params;
	// 5xx and connection errors are the real signal of deadness.
	if resp.StatusCode >= 500 {
		return false, "http_5xx"
	}
	return true, "http_ok"
}

// classifyHTTPError returns a short tag describing the network failure
// without dumping the full error text into log fields.
func classifyHTTPError(err error) string {
	s := err.Error()
	switch {
	case containsCI(s, "timeout"), containsCI(s, "deadline exceeded"):
		return "timeout"
	case containsCI(s, "no such host"):
		return "dns_no_such_host"
	case containsCI(s, "connection refused"):
		return "connection_refused"
	case containsCI(s, "no route to host"):
		return "no_route_to_host"
	case containsCI(s, "tls"):
		return "tls_error"
	default:
		return "other_error"
	}
}

func containsCI(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// probeUDP performs a BEP-15 connect handshake. Returns true if the tracker
// answered with a valid connect response (action=0, matching transaction_id).
func (p *TrackerProber) probeUDP(addr string) (bool, string) {
	conn, err := net.DialTimeout("udp", addr, p.timeout)
	if err != nil {
		return false, classifyHTTPError(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(p.timeout)); err != nil {
		return false, "set_deadline_error"
	}

	var req [16]byte
	binary.BigEndian.PutUint64(req[0:8], 0x41727101980) // BEP-15 magic
	binary.BigEndian.PutUint32(req[8:12], 0)            // action = connect
	txID := rand.Uint32()
	binary.BigEndian.PutUint32(req[12:16], txID)

	if _, err := conn.Write(req[:]); err != nil {
		return false, "udp_write_error"
	}
	var resp [16]byte
	n, err := conn.Read(resp[:])
	if err != nil {
		return false, classifyHTTPError(err)
	}
	if n < 16 {
		return false, "udp_short_response"
	}
	if binary.BigEndian.Uint32(resp[0:4]) != 0 {
		return false, "udp_wrong_action"
	}
	if binary.BigEndian.Uint32(resp[4:8]) != txID {
		return false, "udp_wrong_txid"
	}
	return true, "udp_ok"
}
