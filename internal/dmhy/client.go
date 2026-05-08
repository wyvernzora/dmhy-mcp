package dmhy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed/rss"
)

const (
	DefaultBaseURL      = "https://share.dmhy.org/topics/rss/rss.xml"
	DefaultUserAgent    = "karasu-dmhy-mcp/0.1"
	DefaultConcurrency  = 2
	DefaultMinInterval  = 500 * time.Millisecond
	DefaultTimeout      = 15 * time.Second
	DefaultRetryBackoff = 2 * time.Second
)

// Query holds the upstream filter parameters. Zero values are not sent.
type Query struct {
	Keyword string
	SortID  int
	TeamID  int
	Order   string
}

// Config configures a Client. Zero values fall back to package defaults.
type Config struct {
	BaseURL      string
	UserAgent    string
	Concurrency  int
	MinInterval  time.Duration
	Timeout      time.Duration
	RetryBackoff time.Duration
	Logger       *slog.Logger
}

// Client is the politeness-wrapped DMHY HTTP client. Safe for concurrent use.
type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
	logger     *slog.Logger

	parser *rss.Parser

	sem chan struct{}

	mu          sync.Mutex
	lastCall    time.Time
	minInterval time.Duration

	retryBackoff time.Duration

	tzWarnOnce sync.Once

	asiaShanghai *time.Location
}

// NewClient builds a Client from cfg, applying defaults for zero fields.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = DefaultMinInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = DefaultRetryBackoff
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return &Client{
		httpClient:   &http.Client{Timeout: cfg.Timeout},
		userAgent:    cfg.UserAgent,
		baseURL:      cfg.BaseURL,
		logger:       cfg.Logger,
		parser:       &rss.Parser{},
		sem:          make(chan struct{}, cfg.Concurrency),
		minInterval:  cfg.MinInterval,
		retryBackoff: cfg.RetryBackoff,
		asiaShanghai: loc,
	}
}

// BuildURL composes the upstream URL for q. Zero/empty parameters are omitted
// so we don't pin defaults DMHY may interpret unexpectedly.
func (c *Client) BuildURL(q Query) string {
	v := url.Values{}
	if q.Keyword != "" {
		v.Set("keyword", q.Keyword)
	}
	if q.SortID != 0 {
		v.Set("sort_id", strconv.Itoa(q.SortID))
	}
	if q.TeamID != 0 {
		v.Set("team_id", strconv.Itoa(q.TeamID))
	}
	if q.Order != "" {
		v.Set("order", q.Order)
	}
	if len(v) == 0 {
		return c.baseURL
	}
	return c.baseURL + "?" + v.Encode()
}

// Fetch executes the upstream call and returns parsed releases. Errors are
// always *ToolError so the caller can surface them directly to the MCP layer.
func (c *Client) Fetch(ctx context.Context, q Query) ([]Release, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	target := c.BuildURL(q)

	body, terr := c.fetchOnce(ctx, target)
	if terr != nil && terr.Retriable {
		t := time.NewTimer(c.retryBackoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, &ToolError{Code: CodeUpstreamUnavailable, Message: ctx.Err().Error(), Retriable: true}
		case <-t.C:
		}
		body, terr = c.fetchOnce(ctx, target)
	}
	if terr != nil {
		return nil, terr
	}
	defer body.Close()

	feed, err := c.parser.Parse(body)
	if err != nil {
		return nil, &ToolError{
			Code:      CodeUpstreamMalformed,
			Message:   "failed to parse RSS: " + err.Error(),
			Retriable: true,
		}
	}

	out := make([]Release, 0, len(feed.Items))
	for _, item := range feed.Items {
		out = append(out, c.toRelease(item))
	}
	return out, nil
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return &ToolError{Code: CodeUpstreamUnavailable, Message: ctx.Err().Error(), Retriable: true}
	}
	c.mu.Lock()
	wait := time.Until(c.lastCall.Add(c.minInterval))
	c.mu.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			<-c.sem
			return &ToolError{Code: CodeUpstreamUnavailable, Message: ctx.Err().Error(), Retriable: true}
		}
	}
	c.mu.Lock()
	c.lastCall = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *Client) release() { <-c.sem }

func (c *Client) fetchOnce(ctx context.Context, target string) (io.ReadCloser, *ToolError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &ToolError{Code: CodeInternal, Message: "build request: " + err.Error(), Retriable: false}
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9, */*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ToolError{Code: CodeUpstreamUnavailable, Message: err.Error(), Retriable: true}
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, &ToolError{
			Code:      CodeUpstreamBlocked,
			Message:   fmt.Sprintf("upstream returned %d", resp.StatusCode),
			Retriable: false,
		}
	}
	if resp.StatusCode >= 500 {
		resp.Body.Close()
		return nil, &ToolError{
			Code:      CodeUpstreamUnavailable,
			Message:   fmt.Sprintf("upstream returned %d", resp.StatusCode),
			Retriable: true,
		}
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, &ToolError{
			Code:      CodeInternal,
			Message:   fmt.Sprintf("upstream returned %d", resp.StatusCode),
			Retriable: false,
		}
	}
	return resp.Body, nil
}

func (c *Client) toRelease(item *rss.Item) Release {
	r := Release{
		Title:       item.Title,
		Link:        item.Link,
		Author:      item.Author,
		Description: item.Description,
	}
	if item.GUID != nil && item.GUID.Value != "" {
		r.GUID = item.GUID.Value
	} else {
		r.GUID = item.Link
	}
	if item.Enclosure != nil {
		r.Magnet = item.Enclosure.URL
		r.InfoHash = parseInfoHash(item.Enclosure.URL)
	}
	switch {
	case item.PubDate != "" && !hasTimezone(item.PubDate):
		r.PubDate = c.parsePubDateFallback(item.PubDate)
	case item.PubDateParsed != nil:
		r.PubDate = *item.PubDateParsed
	case item.PubDate != "":
		r.PubDate = c.parsePubDateFallback(item.PubDate)
	}
	if len(item.Categories) > 0 {
		cat := item.Categories[0]
		r.CategoryID = parseCategoryID(cat.Domain)
		r.CategoryText = cat.Value
	}
	return r
}

// parsePubDateFallback handles the rare case where gofeed cannot parse the
// pubDate. We treat the timestamp as Asia/Shanghai (DMHY's source timezone)
// and emit a single warn log per process to flag the schema drift.
func (c *Client) parsePubDateFallback(s string) time.Time {
	c.tzWarnOnce.Do(func() {
		c.logger.Warn("dmhy pubDate missing timezone; assuming Asia/Shanghai", "value", s)
	})
	layouts := []string{
		"Mon, 02 Jan 2006 15:04:05",
		"02 Jan 2006 15:04:05",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, c.asiaShanghai); err == nil {
			return t
		}
	}
	return time.Time{}
}

// tzSuffix matches a recognizable timezone token at the end of a date string:
// numeric offsets (+0800, -05:00), single-letter Z, or alphabetic abbreviations
// (UTC, GMT, EST, ...).
var tzSuffix = regexp.MustCompile(`(?:[+-]\d{2}:?\d{2}|Z|[A-Za-z]{2,5})\s*$`)

// hasTimezone reports whether s ends with a recognizable timezone token. We
// use this to distinguish raw upstream timestamps that need the Asia/Shanghai
// fallback from those gofeed already parsed correctly.
func hasTimezone(s string) bool {
	return tzSuffix.MatchString(strings.TrimSpace(s))
}

// parseInfoHash extracts the lowercased btih hash from a magnet URI. Returns
// "" when no usable hash is present.
func parseInfoHash(magnet string) string {
	if !strings.HasPrefix(magnet, "magnet:") {
		return ""
	}
	q := strings.TrimPrefix(magnet, "magnet:")
	q = strings.TrimPrefix(q, "?")
	values, err := url.ParseQuery(q)
	if err != nil {
		return ""
	}
	for _, xt := range values["xt"] {
		const prefix = "urn:btih:"
		if !strings.HasPrefix(xt, prefix) {
			continue
		}
		hash := strings.ToLower(strings.TrimPrefix(xt, prefix))
		if isValidInfoHash(hash) {
			return hash
		}
	}
	return ""
}

func isValidInfoHash(s string) bool {
	switch len(s) {
	case 40:
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
		return true
	case 32:
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// parseCategoryID pulls the trailing integer out of a domain attribute like
// "https://share.dmhy.org/topics/list/sort_id/2".
func parseCategoryID(domain string) int {
	if domain == "" {
		return 0
	}
	parts := strings.Split(domain, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "sort_id" {
			if n, err := strconv.Atoi(parts[i+1]); err == nil {
				return n
			}
		}
	}
	if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return n
	}
	return 0
}

// Dedup collapses entries sharing an InfoHash, preserving first-seen order.
// Entries with empty InfoHash are kept verbatim. Returns the deduped slice and
// a flag indicating whether any duplicates were removed.
func Dedup(items []Release) ([]Release, bool) {
	seen := make(map[string]struct{}, len(items))
	out := make([]Release, 0, len(items))
	dropped := false
	for _, it := range items {
		if it.InfoHash == "" {
			out = append(out, it)
			continue
		}
		if _, ok := seen[it.InfoHash]; ok {
			dropped = true
			continue
		}
		seen[it.InfoHash] = struct{}{}
		out = append(out, it)
	}
	return out, dropped
}
