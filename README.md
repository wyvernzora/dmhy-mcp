# dmhy-mcp

Thin MCP server wrapping the DMHY (動漫花園) RSS feed. Built on the official
[modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk).

## Tools

| Tool | Purpose |
| --- | --- |
| `search_releases` | Filtered query (`keyword`, `category`, `order`, `limit`, `offset`). At least one filter required. Start with a short keyword fragment and refine based on the returned titles — release titles mix scripts and tags unpredictably, so long queries usually miss. Paginate via `offset` when `has_more` is true; dedup across pages by `info_hash`. |
| `get_recent` | Latest releases without a keyword. Useful for browsing a category or group. |
| `get_magnets` | Resolve `info_hash` values from prior search / recent results into magnet URIs. Search results omit magnets to keep responses small; call this only for the few releases the agent actually wants to download. Returns a map of `info_hash → magnet` plus a list of any hashes not in the in-memory cache. |

`category` is an enum: `anime` (DMHY sort_id 2) or `anime_season` (sort_id 31).
Search / recent results carry `category`, `title`, `info_hash`, and
`pub_date` — the magnet URI is intentionally omitted (tracker lists make
magnets large and noisy). Fetch magnets via `get_magnets` for the chosen
releases. Returned magnets are pre-pruned of dead trackers using a
background BEP-15 / HTTP probe cache, so they stay short and only carry
trackers that actually responded recently.

The server intentionally does **not** parse release titles — the team tag
is already inside the title and the agent is expected to read it.

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
