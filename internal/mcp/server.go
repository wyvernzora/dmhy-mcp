package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/dmhy-mcp/internal/dmhy"
)

const (
	serverName  = "dmhy-mcp"
	mcpEndpoint = "/mcp"
	healthPath  = "/healthz"

	// maxRequestBytes caps incoming HTTP bodies. The MCP wire protocol carries
	// only small JSON-RPC messages; anything larger is almost certainly abuse
	// or a misconfigured client.
	maxRequestBytes = 1 << 20 // 1 MiB

	// httpShutdownGrace must be at least the upstream HTTP timeout so an
	// in-flight tool call has a chance to finish during graceful shutdown.
	httpShutdownGrace = 20 * time.Second
)

// New constructs the MCP server with all dmhy tools registered.
func New(client *dmhy.Client, logger *slog.Logger, version string) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    serverName,
		Version: version,
	}, nil)
	Register(s, client, logger)
	return s
}

// RunStdio runs the server on the stdio transport. Blocks until ctx is done or
// the transport returns.
func RunStdio(ctx context.Context, s *mcpsdk.Server) error {
	return s.Run(ctx, &mcpsdk.StdioTransport{})
}

// RunHTTP starts the HTTP transport plus a /healthz endpoint. Blocks until ctx
// is cancelled, then returns after a graceful shutdown.
func RunHTTP(ctx context.Context, s *mcpsdk.Server, addr string, logger *slog.Logger) error {
	mux := http.NewServeMux()

	// The streamable HTTP transport spec uses a single endpoint for POST
	// (client→server requests) and GET (server→client SSE). Mounting the SDK
	// handler only at the exact path keeps the surface narrow.
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
	mux.Handle(mcpEndpoint, capBodySize(mcpHandler))

	mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind synchronously so we only log "listening" once the port is actually
	// accepting connections, then serve in the background.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger.Info("http transport listening", "addr", ln.Addr().String(), "endpoint", mcpEndpoint)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// capBodySize wraps h to reject request bodies larger than maxRequestBytes.
// Streaming responses (SSE) are unaffected because we cap on the request side.
func capBodySize(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		h.ServeHTTP(w, r)
	})
}
