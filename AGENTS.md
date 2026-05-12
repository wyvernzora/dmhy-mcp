# AGENTS.md

Drop-in operating instructions for coding agents working on **dmhy-mcp**. Read the user-global rules first:

- `~/.agents/AGENTS.md` — universal agent-behavior rules (non-negotiables, simplicity, surgical changes, communication style, grilling, etc.)
- `~/.agents/go.md` — Go engineering rules (loaded because this repo has `go.mod`)

This file holds project-specific context, learnings, and overrides only. Rules in the global files apply unless explicitly contradicted here.

---

## 1. Project context

### About dmhy-mcp

- **Name:** dmhy-mcp.
- **Domain:** MCP server that proxies DMHY anime RSS queries.
- **Tools:** `search_releases`, `get_recent`, `get_magnets`.
- **Transports:** stdio (default), streamable HTTP (`--transport=http --addr=:8080`, MCP mounted at `/mcp`).
- **No auth, no REST API, no web UI.**
- **Distribution:** Go binary, Docker container.

### Stack

- **Language:** Go 1.25.0+. Pinned in `go.mod`.
- **Entry point:** `cmd/dmhy-mcp/main.go` — flag-driven, env fallbacks for all flags (prefix `DMHY_`).
- **MCP SDK:** `github.com/modelcontextprotocol/go-sdk`; streamable HTTP handler at `/mcp`, health check at `/healthz`.
- **RSS client:** `internal/dmhy` — concurrency-limited, rate-limited, retrying RSS fetcher.
- **Server wiring:** `internal/mcp/server.go` (transport setup + HTTP handler), `internal/mcp/tools.go` (tool definitions).

### Commands

```sh
go run ./cmd/dmhy-mcp          # run from source (stdio)
go test ./...                  # full test suite
go build -o bin/dmhy-mcp ./cmd/dmhy-mcp
make devserver-build           # build dev image (hot-reload + inspector)
make devserver-run             # start dev container
```

### Relevant flags / env vars

```
--transport=http               # enables HTTP transport (DMHY_TRANSPORT)
--addr=:8080                   # listen address for HTTP (DMHY_ADDR)
--upstream-base=<url>          # DMHY RSS base URL (DMHY_UPSTREAM_BASE)
--log-level=debug              # structured JSON log level (DMHY_LOG_LEVEL)
--upstream-concurrency=2       # max parallel RSS requests (DMHY_UPSTREAM_CONCURRENCY)
--upstream-min-interval=500ms  # min delay between requests (DMHY_UPSTREAM_MIN_INTERVAL)
--upstream-timeout=15s         # per-request timeout (DMHY_UPSTREAM_TIMEOUT)
```

---

## 2. Project Learnings

**Accumulated corrections. This section is for the agent to maintain, not just the human.**

When the user corrects your approach, append a one-line rule here before ending the session. Write it concretely ("Always use X for Y"), never abstractly ("be careful with Y"). If an existing line already covers the correction, tighten it instead of adding a new one.

- (empty)
