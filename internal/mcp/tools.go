// Package mcp wires the dmhy client into MCP tool handlers and transports.
package mcp

import (
	"context"
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
	Query     QueryEcho       `json:"query"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated"`
	Results   []dmhy.Release  `json:"results"`
}

// CategoriesOutput is the structured output shape for list_categories.
type CategoriesOutput struct {
	Categories []dmhy.Category `json:"categories"`
}

// Register adds all dmhy tools to the given server.
func Register(s *mcpsdk.Server, client *dmhy.Client, logger *slog.Logger) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "search_releases",
			Description: "Query the DMHY RSS feed with keyword, category, and team filters. At least one filter must be set; otherwise use get_recent.",
		},
		withLogging("search_releases", logger, searchHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_recent",
			Description: "Return the most recent DMHY releases without requiring a keyword. Useful for browsing a category or group.",
		},
		withLogging("get_recent", logger, recentHandler(client)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_categories",
			Description: "Return the static DMHY category table (sort_id → zh-Hant text and rough English label).",
		},
		withLogging("list_categories", logger, categoriesHandler()),
	)
}

// withLogging wraps a tool handler with structured logging at info level. The
// keyword field is intentionally omitted from info logs (debug only) because
// keywords reflect user search history.
func withLogging[I, O any](name string, logger *slog.Logger, h mcpsdk.ToolHandlerFor[I, O]) mcpsdk.ToolHandlerFor[I, O] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		start := time.Now()
		logger.Debug("tool call start", "tool", name, "input", in)
		res, out, err := h(ctx, req, in)
		dur := time.Since(start)
		errCode := ""
		if res != nil && res.IsError {
			errCode = extractErrCode(res)
		} else if err != nil {
			errCode = string(dmhy.CodeInternal)
		}
		logger.Info("tool call",
			"tool", name,
			"duration_ms", dur.Milliseconds(),
			"err_code", errCode,
		)
		return res, out, err
	}
}

func extractErrCode(res *mcpsdk.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			var te dmhy.ToolError
			if err := unmarshalErrCode(tc.Text, &te); err == nil && te.Code != "" {
				return string(te.Code)
			}
		}
	}
	return string(dmhy.CodeInternal)
}

func searchHandler(client *dmhy.Client) mcpsdk.ToolHandlerFor[SearchInput, ReleasesOutput] {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in SearchInput) (*mcpsdk.CallToolResult, ReleasesOutput, error) {
		if in.Keyword == "" && in.SortID == 0 && in.TeamID == 0 {
			return errorResult(&dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   "search_releases requires at least one of keyword, sort_id, or team_id; use get_recent for the unfiltered firehose",
				Retriable: false,
			}), ReleasesOutput{}, nil
		}
		order := in.Order
		switch order {
		case "", "date-desc":
			order = "date-desc"
		case "date-asc":
		default:
			return errorResult(&dmhy.ToolError{
				Code:      dmhy.CodeInvalidArgument,
				Message:   fmt.Sprintf("order %q invalid; expected one of date-desc, date-asc", in.Order),
				Retriable: false,
			}), ReleasesOutput{}, nil
		}
		limit := normalizeLimit(in.Limit, defaultSearchLimit)
		out, terr := runFetch(ctx, client, dmhy.Query{
			Keyword: in.Keyword,
			SortID:  in.SortID,
			TeamID:  in.TeamID,
			Order:   order,
		}, limit, in.IncludeDescription)
		if terr != nil {
			return errorResult(terr), ReleasesOutput{}, nil
		}
		out.Query = QueryEcho{
			Keyword: in.Keyword,
			SortID:  in.SortID,
			TeamID:  in.TeamID,
			Order:   order,
		}
		return nil, out, nil
	}
}

func recentHandler(client *dmhy.Client) mcpsdk.ToolHandlerFor[RecentInput, ReleasesOutput] {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in RecentInput) (*mcpsdk.CallToolResult, ReleasesOutput, error) {
		limit := normalizeLimit(in.Limit, defaultRecentLimit)
		out, terr := runFetch(ctx, client, dmhy.Query{
			SortID: in.SortID,
			TeamID: in.TeamID,
			Order:  "date-desc",
		}, limit, false)
		if terr != nil {
			return errorResult(terr), ReleasesOutput{}, nil
		}
		out.Query = QueryEcho{
			SortID: in.SortID,
			TeamID: in.TeamID,
			Order:  "date-desc",
		}
		return nil, out, nil
	}
}

func categoriesHandler() mcpsdk.ToolHandlerFor[CategoriesInput, CategoriesOutput] {
	return func(_ context.Context, _ *mcpsdk.CallToolRequest, _ CategoriesInput) (*mcpsdk.CallToolResult, CategoriesOutput, error) {
		return nil, CategoriesOutput{Categories: dmhy.Categories}, nil
	}
}

func runFetch(ctx context.Context, client *dmhy.Client, q dmhy.Query, limit int, includeDescription bool) (ReleasesOutput, *dmhy.ToolError) {
	releases, err := client.Fetch(ctx, q)
	if err != nil {
		var te *dmhy.ToolError
		if asToolError(err, &te) {
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
