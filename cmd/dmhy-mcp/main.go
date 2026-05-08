// Command dmhy-mcp is the DMHY MCP server entrypoint.
//
// It exposes three tools (search_releases, get_recent, list_categories) over
// either the stdio transport (for local agents) or the streamable HTTP
// transport (for k8s deployments). All configuration is via flags, each
// honoring an environment-variable fallback so container deployments can
// configure the binary without crafting an args list.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	// time/tzdata bakes the IANA zoneinfo database into the binary so the
	// Asia/Shanghai pubDate fallback works in distroless/scratch images that
	// lack /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/wyvernzora/dmhy-mcp/internal/dmhy"
	mcppkg "github.com/wyvernzora/dmhy-mcp/internal/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dmhy-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		transport     = stringFlag("transport", "DMHY_TRANSPORT", "stdio", "transport: stdio or http")
		addr          = stringFlag("addr", "DMHY_ADDR", ":8080", "listen address (http transport only)")
		userAgent     = stringFlag("user-agent", "DMHY_USER_AGENT", dmhy.DefaultUserAgent, "User-Agent sent to upstream")
		upstreamBase  = stringFlag("upstream-base", "DMHY_UPSTREAM_BASE", dmhy.DefaultBaseURL, "upstream RSS endpoint")
		concurrency   = intFlag("upstream-concurrency", "DMHY_UPSTREAM_CONCURRENCY", dmhy.DefaultConcurrency, "max concurrent upstream requests")
		minInterval   = durationFlag("upstream-min-interval", "DMHY_UPSTREAM_MIN_INTERVAL", dmhy.DefaultMinInterval, "minimum interval between upstream requests")
		upstreamToTO  = durationFlag("upstream-timeout", "DMHY_UPSTREAM_TIMEOUT", dmhy.DefaultTimeout, "per-request upstream HTTP timeout")
		logLevel      = stringFlag("log-level", "DMHY_LOG_LEVEL", "info", "log level: debug, info, warn, error")
	)
	flag.Parse()

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	client := dmhy.NewClient(dmhy.Config{
		BaseURL:     *upstreamBase,
		UserAgent:   *userAgent,
		Concurrency: *concurrency,
		MinInterval: *minInterval,
		Timeout:     *upstreamToTO,
		Logger:      logger,
	})
	server := mcppkg.New(client, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		logger.Info("starting dmhy-mcp", "transport", "stdio", "version", "0.1.0")
		return mcppkg.RunStdio(ctx, server)
	case "http":
		logger.Info("starting dmhy-mcp", "transport", "http", "addr", *addr, "version", "0.1.0")
		return mcppkg.RunHTTP(ctx, server, *addr, logger)
	default:
		return fmt.Errorf("invalid --transport %q (want stdio or http)", *transport)
	}
}

func stringFlag(name, env, def, usage string) *string {
	if v := os.Getenv(env); v != "" {
		def = v
	}
	return flag.String(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func intFlag(name, env string, def int, usage string) *int {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			def = n
		}
	}
	return flag.Int(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func durationFlag(name, env string, def time.Duration, usage string) *time.Duration {
	if v := os.Getenv(env); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			def = d
		}
	}
	return flag.Duration(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid --log-level %q", s)
	}
}
