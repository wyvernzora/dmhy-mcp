// Package dmhy provides types and an HTTP client for the DMHY (動漫花園) RSS feed.
package dmhy

import (
	"encoding/json"
	"time"
)

// Release is the JSON-serializable shape returned to MCP clients for a single
// torrent release.
type Release struct {
	GUID         string    `json:"guid"`
	Title        string    `json:"title"`
	Link         string    `json:"link"`
	PubDate      time.Time `json:"pub_date"`
	Magnet       string    `json:"magnet"`
	InfoHash     string    `json:"info_hash"`
	Author       string    `json:"author"`
	CategoryID   int       `json:"category_id"`
	CategoryText string    `json:"category"`
	Description  string    `json:"description,omitempty"`
}

// Category is one entry in the DMHY category table.
type Category struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	EN   string `json:"en"`
}

// Categories is the hardcoded subset of DMHY sort_id values that appear in
// practice. The list_categories tool returns this verbatim. Pass-through of
// unknown sort_id values on input is allowed; this table is informational.
var Categories = []Category{
	{ID: 1, Text: "其他", EN: "other"},
	{ID: 2, Text: "動畫", EN: "anime"},
	{ID: 4, Text: "音樂", EN: "music"},
	{ID: 6, Text: "日劇", EN: "J-drama"},
	{ID: 7, Text: "ＲＡＷ", EN: "raw"},
	{ID: 12, Text: "特攝", EN: "tokusatsu"},
	{ID: 31, Text: "季度全集", EN: "season batch"},
	{ID: 43, Text: "動漫音樂", EN: "anime music"},
}

// ErrCode is a stable string identifier the agent can branch on.
type ErrCode string

const (
	CodeInvalidArgument     ErrCode = "invalid_argument"
	CodeUpstreamUnavailable ErrCode = "upstream_unavailable"
	CodeUpstreamMalformed   ErrCode = "upstream_malformed"
	CodeUpstreamBlocked     ErrCode = "upstream_blocked"
	CodeInternal            ErrCode = "internal"
)

// ToolError is the structured payload returned to MCP clients on tool failure.
// It implements error so it can flow through Go error returns.
type ToolError struct {
	Code      ErrCode `json:"code"`
	Message   string  `json:"message"`
	Retriable bool    `json:"retriable"`
}

func (e *ToolError) Error() string { return string(e.Code) + ": " + e.Message }

// JSON renders the ToolError as a single-line JSON object suitable for
// embedding in a TextContent body.
func (e *ToolError) JSON() string {
	b, err := json.Marshal(e)
	if err != nil {
		return `{"code":"internal","message":"failed to marshal error","retriable":false}`
	}
	return string(b)
}
