# dmhy-mcp

MCP server wrapping the [DMHY](https://share.dmhy.org) (動漫花園) anime RSS feed.

## Tools

| Tool | Description |
| --- | --- |
| `search_releases` | Search releases by keyword, category, order, limit, offset. At least one filter required. |
| `get_recent` | Latest releases without a keyword filter. |
| `get_magnets` | Resolve info hashes from search/recent results into magnet URIs. Results omit magnets by default; call this only for releases you want to download. Dead trackers are pruned via a background BEP-15 probe cache. |

`category`: `anime` (sort_id 2) or `anime_season` (sort_id 31).

`search_releases` and `get_recent` both return a `feed_url` alongside the matched releases — it is the upstream DMHY RSS URL that bakes the same query into a subscribable feed. Pipe verbatim into [qbit-mcp](https://github.com/wyvernzora/qbittorrent-mcp)'s `qbit_subscribe.feed_url` to auto-download ongoing matches of the query.

## Build & run

```sh
go build ./cmd/dmhy-mcp
./dmhy-mcp --transport=stdio
./dmhy-mcp --transport=http --addr=:8080
```

HTTP transport exposes the MCP endpoint at `/mcp` and a k8s liveness probe at `/healthz`.

## Container

```sh
docker build -t dmhy-mcp .
docker run --rm -p 8080:8080 dmhy-mcp           # HTTP on :8080
docker run --rm -i dmhy-mcp --transport=stdio   # stdio
```

## Configuration

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

Tool errors are returned as `IsError: true` with a JSON body:

```json
{ "code": "upstream_unavailable", "message": "...", "retriable": true }
```

Codes: `invalid_argument`, `upstream_unavailable`, `upstream_malformed`, `upstream_blocked`, `internal`.
