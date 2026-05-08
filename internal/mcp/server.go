package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/dmhy-mcp/internal/dmhy"
)

const (
	serverName    = "dmhy-mcp"
	serverVersion = "0.1.0"
	mcpEndpoint   = "/mcp"
	healthPath    = "/healthz"
)

// New constructs the MCP server with all dmhy tools registered.
func New(client *dmhy.Client, logger *slog.Logger) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    serverName,
		Version: serverVersion,
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
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
	mux.Handle(mcpEndpoint, mcpHandler)
	mux.Handle(mcpEndpoint+"/", mcpHandler)
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

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http transport listening", "addr", addr, "endpoint", mcpEndpoint)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// asToolError unwraps the dmhy.ToolError chain returned by client.Fetch.
func asToolError(err error, te **dmhy.ToolError) bool { return errors.As(err, te) }

// unmarshalErrCode parses the JSON-encoded ToolError text content. Exposed as
// a separate function so tools.go can avoid pulling in encoding/json.
func unmarshalErrCode(s string, te *dmhy.ToolError) error {
	return json.Unmarshal([]byte(s), te)
}
