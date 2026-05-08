# dmhy-mcp

Thin MCP server wrapping the DMHY (動漫花園) RSS feed. Built on the official
[modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk).

## Tools

| Tool | Purpose |
| --- | --- |
| `search_releases` | Filtered query (`keyword`, `sort_id`, `team_id`, `order`, `limit`, `include_description`). At least one filter required. |
| `get_recent` | Latest releases without a keyword. Useful for browsing a category or group. |
| `list_categories` | Static DMHY category table (sort_id ↔ zh-Hant/EN labels). |

Releases are returned as raw upstream titles plus magnet, info-hash, pub date,
author, link, and category metadata. The server intentionally does **not** parse
release titles — that is the agent's job.

## Build & run

```sh
go build ./cmd/dmhy-mcp
./dmhy-mcp --transport=stdio
./dmhy-mcp --transport=http --addr=:8080
```

`/healthz` on the HTTP transport returns `200 ok` for k8s probes; the MCP
endpoint is mounted at `/mcp`.

## Container

```sh
docker build -t dmhy-mcp:dev .
docker run --rm -p 8080:8080 dmhy-mcp:dev
```

The image defaults to HTTP on `:8080`. For stdio, override `CMD`:

```sh
docker run --rm -i dmhy-mcp:dev --transport=stdio
```

## Configuration

Each flag also accepts an environment variable, useful for k8s `env:` blocks.

| Flag | Env | Default |
| --- | --- | --- |
| `--transport` | `DMHY_TRANSPORT` | `stdio` |
| `--addr` | `DMHY_ADDR` | `:8080` |
| `--user-agent` | `DMHY_USER_AGENT` | `karasu-dmhy-mcp/0.1` |
| `--upstream-base` | `DMHY_UPSTREAM_BASE` | `https://share.dmhy.org/topics/rss/rss.xml` |
| `--upstream-concurrency` | `DMHY_UPSTREAM_CONCURRENCY` | `2` |
| `--upstream-min-interval` | `DMHY_UPSTREAM_MIN_INTERVAL` | `500ms` |
| `--upstream-timeout` | `DMHY_UPSTREAM_TIMEOUT` | `15s` |
| `--log-level` | `DMHY_LOG_LEVEL` | `info` |

## Errors

Tool failures arrive as `IsError: true` with a JSON `TextContent` body:

```json
{ "code": "upstream_unavailable", "message": "...", "retriable": true }
```

Codes: `invalid_argument`, `upstream_unavailable`, `upstream_malformed`,
`upstream_blocked`, `internal`.
