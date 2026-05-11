package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/dmhy-mcp/internal/dmhy"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/rss-sample.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// startTestSession spins up an in-memory MCP server backed by a fixture-serving
// upstream and returns a connected client session.
func startTestSession(t *testing.T) (*mcpsdk.ClientSession, func()) { //nolint:gocritic
	t.Helper()
	body := fixtureBytes(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write(body)
	}))
	client := dmhy.NewClient(dmhy.Config{
		BaseURL:      upstream.URL,
		MinInterval:  1 * time.Millisecond,
		RetryBackoff: 5 * time.Millisecond,
		Logger:       discardLogger(),
	})
	server := New(client, discardLogger(), "test")

	t1, t2 := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	cleanup := func() {
		_ = cs.Close()
		cancel()
		upstream.Close()
	}
	return cs, cleanup
}

func TestListTools_BothPresent(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("%s: nil InputSchema", tool.Name)
		}
	}
	for _, want := range []string{"search_releases", "get_recent"} {
		if !got[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

func TestSearchReleases_HappyPath(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   2,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	var out ReleasesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 2 {
		t.Errorf("count = %d, want 2", out.Count)
	}
	if !out.HasMore {
		t.Error("has_more should be true (fixture has 3 anime survivors > limit 2)")
	}
	if out.Query.Order != "date-desc" {
		t.Errorf("query.order = %q, want date-desc", out.Query.Order)
	}
	for _, r := range out.Results {
		if r.Title == "" || r.InfoHash == "" {
			t.Errorf("incomplete release: %+v", r)
		}
		if r.Category != "anime" && r.Category != "anime_season" {
			t.Errorf("unexpected category %q", r.Category)
		}
	}
}

func TestSearchReleases_FillsToFullFixture(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   100,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out ReleasesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Fixture has 9 items across 8 sort_ids; only 3 are anime/anime_season.
	if out.Count != 3 {
		t.Errorf("count = %d, want 3 anime survivors", out.Count)
	}
	if out.HasMore {
		t.Error("has_more should be false; full fixture returned")
	}
}

func TestSearchReleases_Pagination(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	page1, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   2,
			"offset":  0,
		},
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	var p1 ReleasesOutput
	if err := decodeStructured(page1, &p1); err != nil {
		t.Fatalf("decode p1: %v", err)
	}
	if p1.Count != 2 || !p1.HasMore {
		t.Errorf("page1: count=%d has_more=%v, want 2/true", p1.Count, p1.HasMore)
	}

	page2, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   2,
			"offset":  2,
		},
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	var p2 ReleasesOutput
	if err := decodeStructured(page2, &p2); err != nil {
		t.Fatalf("decode p2: %v", err)
	}
	// 3 total survivors, offset 2, limit 2 → 1 returned, no more.
	if p2.Count != 1 || p2.HasMore {
		t.Errorf("page2: count=%d has_more=%v, want 1/false", p2.Count, p2.HasMore)
	}

	seen := map[string]bool{}
	for _, r := range p1.Results {
		seen[r.InfoHash] = true
	}
	for _, r := range p2.Results {
		if seen[r.InfoHash] {
			t.Errorf("info_hash %q appears in both pages", r.InfoHash)
		}
	}
}

func TestGetMagnets_RoundTrip(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	// Populate cache via a search.
	searchRes, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   3,
		},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var search ReleasesOutput
	if err := decodeStructured(searchRes, &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(search.Results) == 0 {
		t.Fatal("no results from search")
	}
	hashes := make([]string, 0, len(search.Results))
	for _, r := range search.Results {
		hashes = append(hashes, r.InfoHash)
	}
	hashes = append(hashes, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef") // unknown hash → missing

	mres, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "get_magnets",
		Arguments: map[string]any{"info_hashes": hashes},
	})
	if err != nil {
		t.Fatalf("get_magnets: %v", err)
	}
	if mres.IsError {
		t.Fatalf("get_magnets error: %+v", mres.Content)
	}
	var mout MagnetsOutput
	if err := decodeStructured(mres, &mout); err != nil {
		t.Fatalf("decode magnets: %v", err)
	}
	for _, h := range hashes[:len(hashes)-1] {
		m, ok := mout.Magnets[h]
		if !ok {
			t.Errorf("missing magnet for known hash %q", h)
			continue
		}
		if !strings.HasPrefix(m, "magnet:?xt=urn:btih:") {
			t.Errorf("magnet %q does not start with magnet prefix", m)
		}
	}
	if len(mout.Missing) != 1 || mout.Missing[0] != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Errorf("missing list = %v, want one entry for the fake hash", mout.Missing)
	}
}

func TestGetMagnets_RejectsEmptyInput(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "get_magnets",
		Arguments: map[string]any{"info_hashes": []string{}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	te := decodeToolError(t, res)
	if te.Code != dmhy.CodeInvalidArgument {
		t.Errorf("code = %s, want %s", te.Code, dmhy.CodeInvalidArgument)
	}
}

func TestSearchReleases_RejectsAllZeroFilters(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "search_releases",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	te := decodeToolError(t, res)
	if te.Code != dmhy.CodeInvalidArgument {
		t.Errorf("code = %s, want %s", te.Code, dmhy.CodeInvalidArgument)
	}
	if te.Retriable {
		t.Error("invalid_argument should not be retriable")
	}
	if !strings.Contains(te.Message, "get_recent") {
		t.Errorf("message should hint get_recent: %q", te.Message)
	}
	if !strings.Contains(te.Message, "category") {
		t.Errorf("message should mention category filter: %q", te.Message)
	}
}

func TestSearchReleases_RejectsBadCategory(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"category": "music",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	te := decodeToolError(t, res)
	if te.Code != dmhy.CodeInvalidArgument {
		t.Errorf("code = %s, want %s", te.Code, dmhy.CodeInvalidArgument)
	}
}

func TestSearchReleases_RejectsBadOrder(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "x",
			"order":   "title-asc",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	te := decodeToolError(t, res)
	if te.Code != dmhy.CodeInvalidArgument {
		t.Errorf("code = %s", te.Code)
	}
}

func TestGetRecent_AllowsZeroFilters(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "get_recent",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	var out ReleasesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count == 0 {
		t.Error("expected results")
	}
	if out.Query.Order != "date-desc" {
		t.Errorf("query.order = %q", out.Query.Order)
	}
}

func TestSearchReleases_UpstreamUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	client := dmhy.NewClient(dmhy.Config{
		BaseURL:      upstream.URL,
		MinInterval:  1 * time.Millisecond,
		RetryBackoff: 5 * time.Millisecond,
		Logger:       discardLogger(),
	})
	server := New(client, discardLogger(), "test")
	t1, t2 := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server: %v", err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "search_releases",
		Arguments: map[string]any{"keyword": "anything"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	te := decodeToolError(t, res)
	if te.Code != dmhy.CodeUpstreamUnavailable {
		t.Errorf("code = %s, want %s", te.Code, dmhy.CodeUpstreamUnavailable)
	}
	if !te.Retriable {
		t.Error("upstream_unavailable should be retriable")
	}
}

func TestHTTPTransport_Healthz(t *testing.T) {
	body := fixtureBytes(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	client := dmhy.NewClient(dmhy.Config{
		BaseURL:      upstream.URL,
		MinInterval:  1 * time.Millisecond,
		RetryBackoff: 5 * time.Millisecond,
		Logger:       discardLogger(),
	})
	server := New(client, discardLogger(), "test")

	mux := http.NewServeMux()
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Errorf("body = %q", string(b))
	}
}

func decodeStructured(res *mcpsdk.CallToolResult, dest any) error {
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, dest)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return json.Unmarshal([]byte(tc.Text), dest)
		}
	}
	return io.EOF
}

func decodeToolError(t *testing.T, res *mcpsdk.CallToolResult) dmhy.ToolError {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			var te dmhy.ToolError
			if err := json.Unmarshal([]byte(tc.Text), &te); err == nil && te.Code != "" {
				return te
			}
		}
	}
	t.Fatalf("no decodable ToolError in result: %+v", res.Content)
	return dmhy.ToolError{}
}
