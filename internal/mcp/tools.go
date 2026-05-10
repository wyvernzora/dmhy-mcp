// Package mcp wires the dmhy client into MCP tool handlers and transports.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/dmhy-mcp/internal/dmhy"
)

const (
	defaultSearchLimit = 50
	defaultRecentLimit = 50
	// maxLimit caps a single tool response so it fits inside typical MCP-client
	// output budgets. Callers paginate with offset + dedup by info_hash. The
	// upstream RSS feed itself returns up to ~500 entries per query, so the
	// effective ceiling for a single query is offset+limit ≤ 500.
	maxLimit = 100
)

// categoryToID maps the agent-facing enum value to the DMHY sort_id.
// Only categories the project surfaces are listed; all other DMHY sort_ids
// are not reachable through this server.
var categoryToID = map[string]int{
	"anime":        2,
	"anime_season": 31,
}

// idToCategory is the reverse lookup for translating an upstream sort_id
// back into the enum value used in MCP output. Releases with a sort_id
// outside this table get an empty Category in the response.
var idToCategory = map[int]string{
	2:  "anime",
	31: "anime_season",
}

func resolveCategory(s string) (int, *dmhy.ToolError) {
	if s == "" {
		return 0, nil
	}
	id, ok := categoryToID[s]
	if !ok {
		return 0, &dmhy.ToolError{
			Code:      dmhy.CodeInvalidArgument,
			Message:   fmt.Sprintf("category %q invalid; expected anime or anime_season", s),
			Retriable: false,
		}
	}
	return id, nil
}

// SearchInput is the JSON input shape for the search_releases tool.
type SearchInput struct {
	Keyword  string `json:"keyword,omitempty" jsonschema:"substring search across the upstream title field. Spaces become AND-joined terms (encoded as +). Prefer short, broad fragments — release titles mix CJK and Latin scripts, group sigils, brackets, and codec tags unpredictably, so long compound queries usually miss. Refine after seeing results."`
	Category string `json:"category,omitempty" jsonschema:"DMHY category enum; one of anime, anime_season"`
	Order    string `json:"order,omitempty" jsonschema:"sort order; one of date-desc (default) or date-asc"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max releases to return in this page; default 50, max 100"`
	Offset   int    `json:"offset,omitempty" jsonschema:"page offset into the deduped result set; default 0. When has_more is true, call again with offset += limit. Dedup across pages by info_hash."`
}

// RecentInput is the JSON input shape for the get_recent tool.
type RecentInput struct {
	Category string `json:"category,omitempty" jsonschema:"DMHY category enum; one of anime, anime_season"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max releases to return in this page; default 50, max 100"`
	Offset   int    `json:"offset,omitempty" jsonschema:"page offset into the deduped result set; default 0. When has_more is true, call again with offset += limit. Dedup across pages by info_hash."`
}

// QueryEcho is the resolved query echoed back to the client.
type QueryEcho struct {
	Keyword  string `json:"keyword,omitempty"`
	Category string `json:"category,omitempty"`
	Order    string `json:"order"`
}

// Release is the wire shape sent to MCP clients. Narrower than dmhy.Release —
// only the fields agents need are surfaced. The magnet URI is intentionally
// omitted: tracker lists make magnets large and noisy. Use get_magnets with
// the info_hash to retrieve the rebuilt (dead-tracker-pruned) magnet for the
// few releases the agent actually wants to download.
type Release struct {
	Category string    `json:"category"`
	Title    string    `json:"title"`
	InfoHash string    `json:"info_hash"`
	PubDate  time.Time `json:"pub_date"`
}

// ReleasesOutput is the structured output shape for both search and get_recent.
type ReleasesOutput struct {
	Query   QueryEcho `json:"query"`
	Count   int       `json:"count"`
	HasMore bool      `json:"has_more"`
	Results []Release `json:"results"`
}

// MagnetsInput is the JSON input shape for the get_magnets tool.
type MagnetsInput struct {
	InfoHashes []string `json:"info_hashes" jsonschema:"info_hash values from prior search_releases / get_recent results"`
}

// MagnetsOutput is the structured output shape for get_magnets.
type MagnetsOutput struct {
	Magnets map[string]string `json:"magnets"`
	Missing []string          `json:"missing,omitempty"`
}

// internalHandler is the shape every tool implements internally. The caller
// (wrap) is responsible for translating *dmhy.ToolError into the SDK's error
// result and recording the err code for logs without round-tripping JSON.
type internalHandler[I, O any] func(ctx context.Context, in I) (O, *dmhy.ToolError)

// Register adds all dmhy tools to the given server.
func Register(s *mcpsdk.Server, client *dmhy.Client, logger *slog.Logger) {
	// Both tools are pure reads against an external feed. Spec defaults are
	// pessimistic (readOnly=false, destructive=true) so we set them explicitly
	// to keep agent UIs from warning on every call.
	falseVal := false
	trueVal := true
	readOnly := &mcpsdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &falseVal,
		IdempotentHint:  true,
		OpenWorldHint:   &trueVal,
	}
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name: "search_releases",
			Description: "Query the DMHY RSS feed with keyword and category filters. At least one filter must be set; otherwise use get_recent. " +
				"Keyword script preference, in order: romanized Japanese first (most permissive — many groups embed romaji titles), then Traditional Chinese, then Japanese kana/kanji. Simplified Chinese sometimes matches but is less reliable; English titles almost never match verbatim. " +
				"Start with the shortest fragment likely to be reasonably unique for the show (e.g. \"tenshi\" for \"The Angel Next Door\", \"Honzuki\" for \"Ascendance of a Bookworm\", \"Kuranika\" for \"クラスで２番目に可愛い…\"). Long, fully-spelled, or multi-language queries almost always return zero matches.",
			Annotations: readOnly,
		},
		wrap("search_releases", logger, searchHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_recent",
			Description: "Return the most recent DMHY releases without requiring a keyword. Useful for browsing a category or group.",
			Annotations: readOnly,
		},
		wrap("get_recent", logger, recentHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name: "get_magnets",
			Description: "Resolve info_hash values from prior search_releases / get_recent results into magnet URIs. " +
				"Search results omit magnets to keep responses small; call this only for the few releases you actually want to download. " +
				"Returns a map of info_hash → magnet plus a list of any hashes not in the in-memory cache (re-run the relevant search to repopulate).",
			Annotations: readOnly,
		},
		wrap("get_magnets", logger, magnetsHandler(client)),
	)
}

// wrap adapts an internalHandler into the SDK signature. It records logging
// at info level (without leaking the raw input) and produces a structured
// CallToolResult on error.
func wrap[I, O any](name string, logger *slog.Logger, h internalHandler[I, O]) mcpsdk.ToolHandlerFor[I, O] {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		start := time.Now()
		logger.Debug("tool call start", "tool", name, "input", in)
		out, terr := h(ctx, in)
		dur := time.Since(start)
		errCode := ""
		if terr != nil {
			errCode = string(terr.Code)
		}
		logger.Info("tool call",
			"tool", name,
			"duration_ms", dur.Milliseconds(),
			"err_code", errCode,
		)
		if terr != nil {
			return errorResult(terr), out, nil
		}
		return nil, out, nil
	}
}

func searchHandler(client *dmhy.Client) internalHandler[SearchInput, ReleasesOutput] {
	return func(ctx context.Context, in SearchInput) (ReleasesOutput, *dmhy.ToolError) {
		if in.Keyword == "" && in.Category == "" {
			return ReleasesOutput{}, &dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   "search_releases requires at least one of keyword or category; use get_recent for the unfiltered firehose",
				Retriable: false,
			}
		}
		sortID, terr := resolveCategory(in.Category)
		if terr != nil {
			return ReleasesOutput{}, terr
		}
		switch in.Order {
		case "", "date-desc", "date-asc":
		default:
			return ReleasesOutput{}, &dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   fmt.Sprintf("order %q invalid; expected one of date-desc, date-asc", in.Order),
				Retriable: false,
			}
		}
		echoOrder := in.Order
		if echoOrder == "" {
			echoOrder = "date-desc"
		}
		limit := normalizeLimit(in.Limit, defaultSearchLimit)
		offset := normalizeOffset(in.Offset)
		out, terr := runFetch(ctx, client, dmhy.Query{
			Keyword: in.Keyword,
			SortID:  sortID,
			Order:   in.Order,
		}, limit, offset)
		if terr != nil {
			return out, terr
		}
		out.Query = QueryEcho{
			Keyword:  in.Keyword,
			Category: in.Category,
			Order:    echoOrder,
		}
		return out, nil
	}
}

func recentHandler(client *dmhy.Client) internalHandler[RecentInput, ReleasesOutput] {
	return func(ctx context.Context, in RecentInput) (ReleasesOutput, *dmhy.ToolError) {
		sortID, terr := resolveCategory(in.Category)
		if terr != nil {
			return ReleasesOutput{}, terr
		}
		limit := normalizeLimit(in.Limit, defaultRecentLimit)
		offset := normalizeOffset(in.Offset)
		out, terr := runFetch(ctx, client, dmhy.Query{
			SortID: sortID,
		}, limit, offset)
		if terr != nil {
			return out, terr
		}
		out.Query = QueryEcho{
			Category: in.Category,
			Order:    "date-desc",
		}
		return out, nil
	}
}

func magnetsHandler(client *dmhy.Client) internalHandler[MagnetsInput, MagnetsOutput] {
	return func(_ context.Context, in MagnetsInput) (MagnetsOutput, *dmhy.ToolError) {
		// Always return a non-nil Magnets map; the SDK schema rejects null
		// for object-typed fields even on the error path.
		empty := MagnetsOutput{Magnets: map[string]string{}}
		if len(in.InfoHashes) == 0 {
			return empty, &dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   "info_hashes is required and must be non-empty",
				Retriable: false,
			}
		}
		found, missing := client.GetMagnets(in.InfoHashes)
		if found == nil {
			found = map[string]string{}
		}
		return MagnetsOutput{Magnets: found, Missing: missing}, nil
	}
}

func runFetch(ctx context.Context, client *dmhy.Client, q dmhy.Query, limit, offset int) (ReleasesOutput, *dmhy.ToolError) {
	releases, err := client.Fetch(ctx, q)
	if err != nil {
		var te *dmhy.ToolError
		if errors.As(err, &te) {
			return ReleasesOutput{}, te
		}
		return ReleasesOutput{}, &dmhy.ToolError{
			Code:      dmhy.CodeInternal,
			Message:   err.Error(),
			Retriable: false,
		}
	}
	// Drop releases outside the supported category set. DMHY returns mixed
	// categories on keyword-only queries and occasionally even when a
	// category filter is set; we want anime-only output regardless.
	filtered := releases[:0]
	for _, r := range releases {
		if _, ok := idToCategory[r.CategoryID]; ok {
			filtered = append(filtered, r)
		}
	}
	collected, _ := dmhy.Dedup(filtered)

	total := len(collected)
	var slice []dmhy.Release
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		slice = collected[offset:end]
	}
	hasMore := total > offset+len(slice)

	results := make([]Release, len(slice))
	for i, r := range slice {
		results[i] = Release{
			Category: idToCategory[r.CategoryID],
			Title:    r.Title,
			InfoHash: r.InfoHash,
			PubDate:  r.PubDate,
		}
	}
	return ReleasesOutput{
		Count:   len(results),
		HasMore: hasMore,
		Results: results,
	}, nil
}

func normalizeOffset(in int) int {
	if in < 0 {
		return 0
	}
	return in
}

func normalizeLimit(in, def int) int {
	if in <= 0 {
		return def
	}
	if in > maxLimit {
		return maxLimit
	}
	return in
}

func errorResult(te *dmhy.ToolError) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: te.JSON()},
		},
	}
}
