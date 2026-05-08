## Build stage
FROM golang:1.25-alpine AS build

WORKDIR /src

# Pre-fetch dependencies for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=0.1.0
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/dmhy-mcp \
    ./cmd/dmhy-mcp

## Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/dmhy-mcp /usr/local/bin/dmhy-mcp

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/dmhy-mcp"]
CMD ["--transport=http", "--addr=:8080"]
