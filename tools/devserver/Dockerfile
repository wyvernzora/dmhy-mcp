# Dev container: hot-reload dmhy-mcp (HTTP transport) + bundled
# @modelcontextprotocol/inspector UI.
#
# Build: make devserver-build
# Run:   make devserver-run
FROM golang:1.26.4-alpine

# tini reaps air + inspector subprocesses cleanly when docker stops the
# container; -g (set on ENTRYPOINT) forwards signals to the whole process
# group so backgrounded children die too. netcat-openbsd provides nc for
# port probing in entrypoint.sh. git is required by go install.
RUN apk add --no-cache tini git nodejs npm netcat-openbsd \
 && npm install -g @modelcontextprotocol/inspector@latest \
 && go install github.com/air-verse/air@latest

WORKDIR /src

# Pre-warm the module cache into the image layer. Invalidated only when
# go.mod / go.sum change. The bind mount at runtime overlays /src, but
# the Go module cache lives in $GOPATH/pkg/mod (outside /src) and
# survives the overlay, cutting cold-start build time from ~30s to ~3s.
COPY go.mod go.sum ./
RUN go mod download

# /src is bind-mounted at runtime, overlaying anything we COPY there.
# Stash air config + scripts outside /src so they survive the mount.
COPY tools/devserver/.air.toml     /etc/dmhy/air.toml
COPY tools/devserver/run-dmhy.sh   /usr/local/bin/run-dmhy.sh
COPY tools/devserver/entrypoint.sh /entrypoint.sh
RUN chmod +x /usr/local/bin/run-dmhy.sh /entrypoint.sh

# dmhy-mcp HTTP transport listens on 0.0.0.0 inside the container;
# the Makefile target pins host-side ports to 127.0.0.1.
# Ports offset from kura devserver (8080/8081 + 6274/6277) so both can run
# concurrently. 6374 = inspector UI, 6377 = inspector proxy (browser <-> MCP).
ENV DMHY_ADDR=0.0.0.0:8090 \
    HOST=0.0.0.0 \
    CLIENT_PORT=6374 \
    SERVER_PORT=6377 \
    MCP_AUTO_OPEN_ENABLED=false \
    ALLOWED_ORIGINS=http://localhost:6374,http://127.0.0.1:6374

EXPOSE 8090 6374 6377

ENTRYPOINT ["/sbin/tini", "-g", "--", "/entrypoint.sh"]
