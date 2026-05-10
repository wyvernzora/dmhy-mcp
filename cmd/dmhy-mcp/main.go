// Command dmhy-mcp is the DMHY MCP server entrypoint.
//
// It exposes three tools (search_releases, get_recent, get_magnets) over
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

// version is overridable at link time via -ldflags="-X main.version=...".
var version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dmhy-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	envWarnings := []envWarning{}
	var (
		showVersion      = flag.Bool("version", false, "print version and exit")
		transport        = stringFlag("transport", "DMHY_TRANSPORT", "stdio", "transport: stdio or http")
		addr             = stringFlag("addr", "DMHY_ADDR", ":8080", "listen address (http transport only)")
		userAgent        = stringFlag("user-agent", "DMHY_USER_AGENT", dmhy.DefaultUserAgent, "User-Agent sent to upstream")
		upstreamBase     = stringFlag("upstream-base", "DMHY_UPSTREAM_BASE", dmhy.DefaultBaseURL, "upstream RSS endpoint")
		concurrency      = intFlag("upstream-concurrency", "DMHY_UPSTREAM_CONCURRENCY", dmhy.DefaultConcurrency, "max concurrent upstream requests", &envWarnings)
		minInterval      = durationFlag("upstream-min-interval", "DMHY_UPSTREAM_MIN_INTERVAL", dmhy.DefaultMinInterval, "minimum interval between upstream requests", &envWarnings)
		upstreamTimeout  = durationFlag("upstream-timeout", "DMHY_UPSTREAM_TIMEOUT", dmhy.DefaultTimeout, "per-request upstream HTTP timeout", &envWarnings)
		retryBackoff     = durationFlag("upstream-retry-backoff", "DMHY_UPSTREAM_RETRY_BACKOFF", dmhy.DefaultRetryBackoff, "backoff between upstream retries", &envWarnings)
		logLevel         = stringFlag("log-level", "DMHY_LOG_LEVEL", "info", "log level: debug, info, warn, error")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	for _, w := range envWarnings {
		logger.Warn("invalid env value, falling back to default",
			"env", w.Env,
			"value", w.Value,
			"error", w.Err,
		)
	}

	client := dmhy.NewClient(dmhy.Config{
		BaseURL:      *upstreamBase,
		UserAgent:    *userAgent,
		Concurrency:  *concurrency,
		MinInterval:  *minInterval,
		Timeout:      *upstreamTimeout,
		RetryBackoff: *retryBackoff,
		Logger:       logger,
	})
	defer client.Close()
	server := mcppkg.New(client, logger, version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		logger.Info("starting dmhy-mcp", "transport", "stdio", "version", version)
		return mcppkg.RunStdio(ctx, server)
	case "http":
		logger.Info("starting dmhy-mcp", "transport", "http", "addr", *addr, "version", version)
		return mcppkg.RunHTTP(ctx, server, *addr, logger)
	default:
		return fmt.Errorf("invalid --transport %q (want stdio or http)", *transport)
	}
}

type envWarning struct {
	Env, Value string
	Err        error
}

func stringFlag(name, env, def, usage string) *string {
	if v := os.Getenv(env); v != "" {
		def = v
	}
	return flag.String(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func intFlag(name, env string, def int, usage string, warnings *[]envWarning) *int {
	if v := os.Getenv(env); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			*warnings = append(*warnings, envWarning{Env: env, Value: v, Err: err})
		} else {
			def = n
		}
	}
	return flag.Int(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func durationFlag(name, env string, def time.Duration, usage string, warnings *[]envWarning) *time.Duration {
	if v := os.Getenv(env); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			*warnings = append(*warnings, envWarning{Env: env, Value: v, Err: err})
		} else {
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
