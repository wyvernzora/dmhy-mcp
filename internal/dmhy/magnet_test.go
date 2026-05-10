package dmhy

import (
	"strings"
	"testing"
)

func TestFilterMagnetTrackers_DropsDeadOnes(t *testing.T) {
	in := "magnet:?xt=urn:btih:abcdef0123abcdef0123abcdef0123abcdef0123" +
		"&dn=Some.Title" +
		"&tr=udp%3A%2F%2Falive.example%3A6969" +
		"&tr=udp%3A%2F%2Fdead.example%3A6969" +
		"&tr=http%3A%2F%2Falive2.example%2Fannounce"
	keep := func(t string) bool {
		return !strings.Contains(t, "dead.example")
	}
	out := FilterMagnetTrackers(in, keep)
	if !strings.Contains(out, "alive.example") {
		t.Errorf("alive tracker dropped: %q", out)
	}
	if strings.Contains(out, "dead.example") {
		t.Errorf("dead tracker kept: %q", out)
	}
	if !strings.Contains(out, "alive2.example") {
		t.Errorf("alive2 tracker dropped: %q", out)
	}
	if !strings.HasPrefix(out, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet prefix lost: %q", out)
	}
	if !strings.Contains(out, "dn=") {
		t.Errorf("dn param dropped: %q", out)
	}
}

func TestFilterMagnetTrackers_KeepAll(t *testing.T) {
	in := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&tr=udp%3A%2F%2Fa%3A1&tr=udp%3A%2F%2Fb%3A2"
	out := FilterMagnetTrackers(in, func(string) bool { return true })
	if strings.Count(out, "tr=") != 2 {
		t.Errorf("want 2 trackers, got: %q", out)
	}
}

func TestFilterMagnetTrackers_DropAll(t *testing.T) {
	in := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&tr=udp%3A%2F%2Fa%3A1"
	out := FilterMagnetTrackers(in, func(string) bool { return false })
	if strings.Contains(out, "tr=") {
		t.Errorf("trackers should be empty: %q", out)
	}
}

func TestFilterMagnetTrackers_NotMagnet(t *testing.T) {
	in := "https://example.com/not-a-magnet"
	out := FilterMagnetTrackers(in, func(string) bool { return true })
	if out != in {
		t.Errorf("non-magnet input should pass through: got %q", out)
	}
}

func TestMagnetTrackers_Extract(t *testing.T) {
	in := "magnet:?xt=urn:btih:abc&tr=udp%3A%2F%2Fa%3A1&tr=http%3A%2F%2Fb%2Fa"
	got := MagnetTrackers(in)
	if len(got) != 2 {
		t.Fatalf("want 2 trackers, got %d: %v", len(got), got)
	}
	if got[0] != "udp://a:1" {
		t.Errorf("got[0] = %q", got[0])
	}
	if got[1] != "http://b/a" {
		t.Errorf("got[1] = %q", got[1])
	}
}

func TestMagnetCache_PutGet(t *testing.T) {
	c := newMagnetCache(3)
	c.put("a", "magnet:a", "title-a")
	c.put("b", "magnet:b", "title-b")
	c.put("c", "magnet:c", "title-c")

	if m, _, ok := c.get("a"); !ok || m != "magnet:a" {
		t.Errorf("a: ok=%v m=%q", ok, m)
	}
	// Insert d → should evict the LRU. After get("a"), b is the LRU.
	c.put("d", "magnet:d", "title-d")
	if _, _, ok := c.get("b"); ok {
		t.Error("b should be evicted as LRU")
	}
	if _, _, ok := c.get("a"); !ok {
		t.Error("a should still be cached")
	}
}

func TestMagnetCache_OverwriteRefreshes(t *testing.T) {
	c := newMagnetCache(2)
	c.put("a", "magnet:a-old", "t")
	c.put("a", "magnet:a-new", "t")
	if m, _, ok := c.get("a"); !ok || m != "magnet:a-new" {
		t.Errorf("overwrite failed: ok=%v m=%q", ok, m)
	}
}
