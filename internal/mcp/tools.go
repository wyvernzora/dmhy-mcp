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
	defaultSearchLimit = 100
	defaultRecentLimit = 50
	maxLimit           = 500
)

// SearchInput is the JSON input shape for the search_releases tool.
type SearchInput struct {
	Keyword            string `json:"keyword,omitempty" jsonschema:"substring search across the title field. Spaces are encoded as +"`
	SortID             int    `json:"sort_id,omitempty" jsonschema:"DMHY category id; 0 means all categories. See list_categories for known ids"`
	TeamID             int    `json:"team_id,omitempty" jsonschema:"DMHY team/group id; 0 means all groups"`
	Order              string `json:"order,omitempty" jsonschema:"sort order; one of date-desc (default) or date-asc"`
	Limit              int    `json:"limit,omitempty" jsonschema:"max releases to return; default 100, max 500"`
	IncludeDescription bool   `json:"include_description,omitempty" jsonschema:"include the raw HTML description blob; default false because it is large and low-signal"`
}

// RecentInput is the JSON input shape for the get_recent tool.
type RecentInput struct {
	SortID int `json:"sort_id,omitempty" jsonschema:"DMHY category id; 0 means all categories"`
	TeamID int `json:"team_id,omitempty" jsonschema:"DMHY team/group id; 0 means all groups"`
	Limit  int `json:"limit,omitempty" jsonschema:"max releases to return; default 50, max 500"`
}

// CategoriesInput has no fields; declared for schema completeness.
type CategoriesInput struct{}

// QueryEcho is the resolved query echoed back to the client.
type QueryEcho struct {
	Keyword string `json:"keyword,omitempty"`
	SortID  int    `json:"sort_id"`
	TeamID  int    `json:"team_id"`
	Order   string `json:"order"`
}

// ReleasesOutput is the structured output shape for both search and get_recent.
type ReleasesOutput struct {
	Query     QueryEcho      `json:"query"`
	Count     int            `json:"count"`
	Truncated bool           `json:"truncated"`
	Results   []dmhy.Release `json:"results"`
}

// CategoriesOutput is the structured output shape for list_categories.
type CategoriesOutput struct {
	Categories []dmhy.Category `json:"categories"`
}

// internalHandler is the shape every tool implements internally. The caller
// (wrap) is responsible for translating *dmhy.ToolError into the SDK's error
// result and recording the err code for logs without round-tripping JSON.
type internalHandler[I, O any] func(ctx context.Context, in I) (O, *dmhy.ToolError)

// Register adds all dmhy tools to the given server.
func Register(s *mcpsdk.Server, client *dmhy.Client, logger *slog.Logger) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "search_releases",
			Description: "Query the DMHY RSS feed with keyword, category, and team filters. At least one filter must be set; otherwise use get_recent.",
		},
		wrap("search_releases", logger, searchHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_recent",
			Description: "Return the most recent DMHY releases without requiring a keyword. Useful for browsing a category or group.",
		},
		wrap("get_recent", logger, recentHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_categories",
			Description: "Return the static DMHY category table (sort_id → zh-Hant text and rough English label).",
		},
		wrap("list_categories", logger, categoriesHandler()),
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
		if in.Keyword == "" && in.SortID == 0 && in.TeamID == 0 {
			return ReleasesOutput{}, &dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   "search_releases requires at least one of keyword, sort_id, or team_id; use get_recent for the unfiltered firehose",
				Retriable: false,
			}
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
		out, terr := runFetch(ctx, client, dmhy.Query{
			Keyword: in.Keyword,
			SortID:  in.SortID,
			TeamID:  in.TeamID,
			Order:   in.Order,
		}, limit, in.IncludeDescription)
		if terr != nil {
			return out, terr
		}
		out.Query = QueryEcho{
			Keyword: in.Keyword,
			SortID:  in.SortID,
			TeamID:  in.TeamID,
			Order:   echoOrder,
		}
		return out, nil
	}
}

func recentHandler(client *dmhy.Client) internalHandler[RecentInput, ReleasesOutput] {
	return func(ctx context.Context, in RecentInput) (ReleasesOutput, *dmhy.ToolError) {
		limit := normalizeLimit(in.Limit, defaultRecentLimit)
		out, terr := runFetch(ctx, client, dmhy.Query{
			SortID: in.SortID,
			TeamID: in.TeamID,
		}, limit, false)
		if terr != nil {
			return out, terr
		}
		out.Query = QueryEcho{
			SortID: in.SortID,
			TeamID: in.TeamID,
			Order:  "date-desc",
		}
		return out, nil
	}
}

func categoriesHandler() internalHandler[CategoriesInput, CategoriesOutput] {
	return func(_ context.Context, _ CategoriesInput) (CategoriesOutput, *dmhy.ToolError) {
		return CategoriesOutput{Categories: dmhy.Categories}, nil
	}
}

func runFetch(ctx context.Context, client *dmhy.Client, q dmhy.Query, limit int, includeDescription bool) (ReleasesOutput, *dmhy.ToolError) {
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
	releases, _ = dmhy.Dedup(releases)
	truncated := false
	if len(releases) > limit {
		releases = releases[:limit]
		truncated = true
	}
	if !includeDescription {
		for i := range releases {
			releases[i].Description = ""
		}
	}
	return ReleasesOutput{
		Count:     len(releases),
		Truncated: truncated,
		Results:   releases,
	}, nil
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
