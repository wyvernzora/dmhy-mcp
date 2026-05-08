package dmhy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/rss-sample.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	body := loadFixture(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

func TestParseFixture_AllFieldsPopulate(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})

	rels, err := c.Fetch(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rels) != 5 {
		t.Fatalf("want 5 items, got %d", len(rels))
	}

	first := rels[0]
	if first.Title == "" || !strings.Contains(first.Title, "進撃の巨人") {
		t.Errorf("title not preserved: %q", first.Title)
	}
	if first.Link == "" {
		t.Error("link empty")
	}
	if first.GUID != first.Link {
		t.Errorf("expected GUID==Link, got guid=%q link=%q", first.GUID, first.Link)
	}
	if first.Author != "nyamomo" {
		t.Errorf("author = %q", first.Author)
	}
	if first.CategoryID != 2 {
		t.Errorf("category id = %d", first.CategoryID)
	}
	if first.CategoryText != "動畫" {
		t.Errorf("category text = %q", first.CategoryText)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet not preserved: %q", first.Magnet)
	}
	if first.InfoHash != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("infohash = %q", first.InfoHash)
	}
	wantTime := time.Date(2026, 5, 8, 11, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if !first.PubDate.Equal(wantTime) {
		t.Errorf("pubdate = %v, want %v", first.PubDate, wantTime)
	}
	if first.Description == "" {
		t.Error("description should be set on the parsed Release; the handler decides whether to expose it")
	}
}

func TestParseFixture_Base32MagnetAndManyTrackers(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	rels, err := c.Fetch(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	frieren := rels[1]
	want := strings.ToLower("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	if frieren.InfoHash != want {
		t.Errorf("base32 infohash = %q, want %q", frieren.InfoHash, want)
	}
	if !strings.Contains(frieren.Magnet, "tr.15=") {
		t.Errorf("magnet trackers truncated: %q", frieren.Magnet)
	}
}

func TestParseFixture_PubDateFallback(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	rels, err := c.Fetch(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	last := rels[len(rels)-1]
	if last.PubDate.IsZero() {
		t.Fatal("fallback pubdate should not be zero")
	}
	_, off := last.PubDate.Zone()
	if off != 8*3600 {
		t.Errorf("fallback offset = %d, want 28800", off)
	}
}

func TestBuildURL(t *testing.T) {
	c := NewClient(Config{BaseURL: "https://example.test/rss"})

	cases := []struct {
		name string
		in   Query
		want string
	}{
		{"no params", Query{}, "https://example.test/rss"},
		{"keyword spaces", Query{Keyword: "hello world"}, "https://example.test/rss?keyword=hello+world"},
		{"keyword cjk", Query{Keyword: "進撃 巨人"}, "https://example.test/rss?keyword=%E9%80%B2%E6%92%83+%E5%B7%A8%E4%BA%BA"},
		{"sort_id only", Query{SortID: 2}, "https://example.test/rss?sort_id=2"},
		{"all params", Query{Keyword: "k", SortID: 2, TeamID: 657, Order: "date-asc"}, "https://example.test/rss?keyword=k&order=date-asc&sort_id=2&team_id=657"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.BuildURL(tc.in)
			gu, _ := url.Parse(got)
			wu, _ := url.Parse(tc.want)
			if gu.Path != wu.Path || gu.Host != wu.Host || gu.Query().Encode() != wu.Query().Encode() {
				t.Errorf("BuildURL(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseInfoHash(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"magnet:?xt=urn:btih:ABCDEF1234567890ABCDEF1234567890ABCDEF12", "abcdef1234567890abcdef1234567890abcdef12"},
		{"magnet:?xt=urn:btih:JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&tr=...", "jbswy3dpehpk3pxpjbswy3dpehpk3pxp"},
		{"magnet:?xt=urn:btih:tooshort", ""},
		{"http://not.a.magnet/", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := parseInfoHash(tc.in)
		if got != tc.want {
			t.Errorf("parseInfoHash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseCategoryID(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"https://share.dmhy.org/topics/list/sort_id/2", 2},
		{"https://share.dmhy.org/topics/list/sort_id/31", 31},
		{"", 0},
		{"https://share.dmhy.org/topics/list/", 0},
	}
	for _, tc := range cases {
		got := parseCategoryID(tc.in)
		if got != tc.want {
			t.Errorf("parseCategoryID(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDedup(t *testing.T) {
	in := []Release{
		{InfoHash: "aaa", Title: "first"},
		{InfoHash: "bbb", Title: "second"},
		{InfoHash: "aaa", Title: "duplicate"},
		{InfoHash: "", Title: "no hash 1"},
		{InfoHash: "", Title: "no hash 2"},
	}
	out, dropped := Dedup(in)
	if !dropped {
		t.Error("expected dropped=true")
	}
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	if out[0].Title != "first" || out[1].Title != "second" {
		t.Errorf("order disturbed: %v", out)
	}
	out2, dropped2 := Dedup([]Release{{InfoHash: "x"}, {InfoHash: "y"}})
	if dropped2 || len(out2) != 2 {
		t.Errorf("no-op dedup wrong: out=%v dropped=%v", out2, dropped2)
	}
}

func TestFetch_RetriesOnce_OnFiveHundred(t *testing.T) {
	body := loadFixture(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	start := time.Now()
	rels, err := c.Fetch(context.Background(), Query{})
	dur := time.Since(start)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("no results after retry")
	}
	if dur < 2*time.Second {
		t.Errorf("expected backoff >=2s, got %v", dur)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("call count = %d, want 2", calls)
	}
}

func TestFetch_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	_, err := c.Fetch(context.Background(), Query{})
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("err not ToolError: %T", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected single call, got %d", calls)
	}
}

func TestFetch_BlockedOn429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	_, err := c.Fetch(context.Background(), Query{})
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("err not ToolError: %T", err)
	}
	if te.Code != CodeUpstreamBlocked {
		t.Errorf("code = %s, want %s", te.Code, CodeUpstreamBlocked)
	}
	if te.Retriable {
		t.Error("blocked should be non-retriable")
	}
}

func TestFetch_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, "not xml at all")
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 1 * time.Millisecond})
	_, err := c.Fetch(context.Background(), Query{})
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("err not ToolError: %T", err)
	}
	if te.Code != CodeUpstreamMalformed {
		t.Errorf("code = %s, want %s", te.Code, CodeUpstreamMalformed)
	}
}

func TestFetch_HonorsMinInterval(t *testing.T) {
	body := loadFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, MinInterval: 200 * time.Millisecond})
	if _, err := c.Fetch(context.Background(), Query{}); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	start := time.Now()
	if _, err := c.Fetch(context.Background(), Query{}); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := time.Since(start); got < 150*time.Millisecond {
		t.Errorf("second fetch too soon: %v", got)
	}
}

func TestFetch_SetsUserAgent(t *testing.T) {
	body := loadFixture(t)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, UserAgent: "test-agent/0.0", MinInterval: 1 * time.Millisecond})
	if _, err := c.Fetch(context.Background(), Query{}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if seen != "test-agent/0.0" {
		t.Errorf("user-agent = %q", seen)
	}
}
