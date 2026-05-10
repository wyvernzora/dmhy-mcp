package dmhy

import (
	"net/url"
	"strings"
)

// FilterMagnetTrackers parses a magnet URI, drops every tr= parameter that
// keep returns false for, and returns the rebuilt magnet. Non-tr parameters
// (xt, dn, xl, etc.) are preserved verbatim with their original encoding —
// this avoids the re-escaping round-trip that breaks magnet readers (e.g.
// the colons inside `xt=urn:btih:HASH` must stay literal). Returns the
// original input if the magnet does not parse.
func FilterMagnetTrackers(magnet string, keep func(tracker string) bool) string {
	if !strings.HasPrefix(magnet, "magnet:") {
		return magnet
	}
	q := strings.TrimPrefix(magnet, "magnet:")
	q = strings.TrimPrefix(q, "?")
	if q == "" {
		return magnet
	}

	parts := strings.Split(q, "&")
	out := parts[:0]
	for _, p := range parts {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			out = append(out, p)
			continue
		}
		key := p[:eq]
		// Tracker keys are tr or tr.<n>; both surface tracker URLs.
		if key != "tr" && !strings.HasPrefix(key, "tr.") {
			out = append(out, p)
			continue
		}
		raw := p[eq+1:]
		decoded, err := url.QueryUnescape(raw)
		if err != nil {
			// Keep on parse failure — better to over-include than to corrupt.
			out = append(out, p)
			continue
		}
		if keep(decoded) {
			out = append(out, p)
		}
	}
	return "magnet:?" + strings.Join(out, "&")
}

// MagnetTrackers extracts the tr= values from a magnet URI. Returns nil if
// the magnet does not parse or has no trackers.
func MagnetTrackers(magnet string) []string {
	if !strings.HasPrefix(magnet, "magnet:") {
		return nil
	}
	q := strings.TrimPrefix(magnet, "magnet:")
	q = strings.TrimPrefix(q, "?")
	if q == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(q, "&") {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		key := p[:eq]
		if key != "tr" && !strings.HasPrefix(key, "tr.") {
			continue
		}
		decoded, err := url.QueryUnescape(p[eq+1:])
		if err != nil {
			continue
		}
		out = append(out, decoded)
	}
	return out
}
