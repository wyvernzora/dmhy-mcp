DEVSERVER_IMAGE      ?= dmhy-mcp-devserver
# Ports offset from kura devserver (8080/8081 + 6274/6277) so both can run
# concurrently. Override on the make command line if these collide too.
MCP_DEV_PORT         ?= 8090
INSPECTOR_PORT       ?= 6374
INSPECTOR_PROXY_PORT ?= 6377

.PHONY: build devserver-build devserver-run

build:
	go build -o bin/dmhy-mcp ./cmd/dmhy-mcp

devserver-build:
	docker build -f tools/devserver/Dockerfile -t $(DEVSERVER_IMAGE) .

# Forwards DMHY_UPSTREAM_BASE, DMHY_LOG_LEVEL, MCP_PROXY_AUTH_TOKEN from
# the host shell into the container when set. All host-side port binds pin
# to 127.0.0.1 so they are not reachable from the network.
devserver-run:
	docker run --rm -it \
		-p 127.0.0.1:$(MCP_DEV_PORT):8090 \
		-p 127.0.0.1:$(INSPECTOR_PORT):6374 \
		-p 127.0.0.1:$(INSPECTOR_PROXY_PORT):6377 \
		-v "$(CURDIR):/src" \
		$(if $(DMHY_UPSTREAM_BASE),-e DMHY_UPSTREAM_BASE="$(DMHY_UPSTREAM_BASE)") \
		$(if $(DMHY_LOG_LEVEL),-e DMHY_LOG_LEVEL="$(DMHY_LOG_LEVEL)") \
		$(if $(MCP_PROXY_AUTH_TOKEN),-e MCP_PROXY_AUTH_TOKEN="$(MCP_PROXY_AUTH_TOKEN)") \
		$(DEVSERVER_IMAGE)
