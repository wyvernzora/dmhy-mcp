package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
func startTestSession(t *testing.T) (*mcpsdk.ClientSession, *url.URL, func()) {
	t.Helper()
	body := fixtureBytes(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write(body)
	}))
	upURL, _ := url.Parse(upstream.URL)
	client := dmhy.NewClient(dmhy.Config{BaseURL: upstream.URL, MinInterval: 1 * time.Millisecond, Logger: discardLogger()})
	server := New(client, discardLogger())

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
	return cs, upURL, cleanup
}

func TestListTools_AllThreePresent(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
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
	for _, want := range []string{"search_releases", "get_recent", "list_categories"} {
		if !got[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

func TestListCategories_ReturnsAllEight(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_categories"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res.Content)
	}
	var out CategoriesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Categories) != 8 {
		t.Errorf("got %d categories, want 8", len(out.Categories))
	}
}

func TestSearchReleases_HappyPath(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "進撃",
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
	if !out.Truncated {
		t.Error("truncated should be true (fixture has 5 dedup-survivors > 2)")
	}
	if out.Query.Order != "date-desc" {
		t.Errorf("query.order = %q, want date-desc", out.Query.Order)
	}
	for _, r := range out.Results {
		if r.Description != "" {
			t.Errorf("description should be stripped by default: %q", r.Description)
		}
	}
}

func TestSearchReleases_DescriptionOptIn(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword":             "進撃",
			"include_description": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out ReleasesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, r := range out.Results {
		if r.Description != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one description with include_description=true")
	}
}

func TestSearchReleases_DedupApplied(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "search_releases",
		Arguments: map[string]any{
			"keyword": "anything",
			"limit":   500,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out ReleasesOutput
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Fixture has 5 items, 2 sharing the same infohash; expect 4 unique.
	if out.Count != 4 {
		t.Errorf("count = %d, want 4 after dedup", out.Count)
	}
}

func TestSearchReleases_RejectsAllZeroFilters(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
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
}

func TestSearchReleases_RejectsBadOrder(t *testing.T) {
	cs, _, cleanup := startTestSession(t)
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
	cs, _, cleanup := startTestSession(t)
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
	client := dmhy.NewClient(dmhy.Config{BaseURL: upstream.URL, MinInterval: 1 * time.Millisecond, Logger: discardLogger()})
	server := New(client, discardLogger())
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

	client := dmhy.NewClient(dmhy.Config{BaseURL: upstream.URL, MinInterval: 1 * time.Millisecond, Logger: discardLogger()})
	server := New(client, discardLogger())

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
