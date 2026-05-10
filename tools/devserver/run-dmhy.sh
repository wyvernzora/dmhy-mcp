#!/bin/sh
# air invokes this as the bin path after each successful build.
# The binary lives at /src/tmp/dmhy-mcp (built by air's build.cmd).
# DMHY_ADDR defaults to 0.0.0.0:8090 (set in Dockerfile ENV).

set -e

exec /src/tmp/dmhy-mcp \
  --transport=http \
  --addr="${DMHY_ADDR:-0.0.0.0:8090}" \
  --log-level="${DMHY_LOG_LEVEL:-debug}"
