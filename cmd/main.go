package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"github.com/tamcore/ephemeron/internal/config"
	"github.com/tamcore/ephemeron/internal/health"
	"github.com/tamcore/ephemeron/internal/hooks"
	"github.com/tamcore/ephemeron/internal/reaper"
	recoverlib "github.com/tamcore/ephemeron/internal/recover"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
	"github.com/tamcore/ephemeron/internal/registry"
	"github.com/tamcore/ephemeron/internal/web"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "ephemeron",
		Short: "Ephemeral container registry manager",
	}

	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(reapCmd())
	rootCmd.AddCommand(recoverCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newConfig(logger *slog.Logger) *config.Config {
	return &config.Config{
		Port:                   envInt(logger, "PORT", 8000),
		InternalPort:           envInt(logger, "INTERNAL_PORT", 9090),
		RedisURL:               envStr("REDIS_URL", envStr("REDISCLOUD_URL", "redis://localhost:6379")),
		HookToken:              envStr("HOOK_TOKEN", ""),
		RegistryURL:            envStr("REGISTRY_URL", "http://localhost:5000"),
		Hostname:               envStr("HOSTNAME_OVERRIDE", "localhost"),
		DefaultTTL:             envDuration(logger, "DEFAULT_TTL", time.Hour),
		MaxTTL:                 envDuration(logger, "MAX_TTL", 24*time.Hour),
		ReapInterval:           envDuration(logger, "REAP_INTERVAL", time.Minute),
		LogFormat:              envStr("LOG_FORMAT", "json"),
		ImmutableTagPatterns:   envStrSlice("IMMUTABLE_TAG_PATTERNS", nil),
		HealthFailureThreshold: envInt(logger, "HEALTH_FAILURE_THRESHOLD", 3),
	}
}

func setupLogger(format string) *slog.Logger {
	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	return slog.New(handler)
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the webhook server, reaper loop, and landing page",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(envStr("LOG_FORMAT", "json"))
			cfg := newConfig(logger)
			if err := cfg.Validate(); err != nil {
				return err
			}

			rdb, err := redisclient.New(cfg.RedisURL)
			if err != nil {
				return fmt.Errorf("connecting to redis: %w", err)
			}
			defer func() { _ = rdb.Close() }()

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()

			if err := rdb.Ping(ctx); err != nil {
				return fmt.Errorf("redis ping failed: %w", err)
			}
			logger.Info("connected to redis")

			// Auto-recover if Redis is not initialized.
			reg := registry.New(cfg.RegistryURL)
			rec := recoverlib.New(rdb, reg, cfg.DefaultTTL, cfg.MaxTTL, logger.With("component", "recover"))
			if err := rec.RunIfNeeded(ctx); err != nil {
				logger.Error("auto-recovery failed", "error", err)
			}

			// Start reaper in background.
			healthChecker := health.New(cfg.HealthFailureThreshold, logger.With("component", "health"))
			r := reaper.New(rdb, cfg.RegistryURL, logger.With("component", "reaper"), reaper.WithHealthReporter(healthChecker))
			go r.RunLoop(ctx, cfg.ReapInterval)

			// Set up public HTTP routes (webhook + landing page).
			mux := http.NewServeMux()

			hookHandler := hooks.NewHandler(
				rdb, reg, cfg.HookToken, cfg.DefaultTTL, cfg.MaxTTL,
				cfg.ImmutableTagPatterns,
				logger.With("component", "hooks"),
			)
			mux.Handle("POST /v1/hook/registry-event", hookHandler)

			webHandler, err := web.NewHandler(cfg.Hostname, cfg.DefaultTTL, cfg.MaxTTL, version, logger.With("component", "web"))
			if err != nil {
				return fmt.Errorf("creating web handler: %w", err)
			}
			mux.Handle("GET /{$}", webHandler)

			// Set up internal HTTP routes (probes + metrics).
			internalMux := http.NewServeMux()
			internalMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
				if !healthChecker.IsHealthy() {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"status":"unhealthy","reason":"registry unreachable"}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			})
			internalMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
				if err := rdb.Ping(r.Context()); err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"status":"not ready"}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			})
			internalMux.Handle("GET /metrics", promhttp.Handler())

			srv := &http.Server{
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			internalSrv := &http.Server{
				Handler:           internalMux,
				ReadHeaderTimeout: 5 * time.Second,
			}

			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
			if err != nil {
				return fmt.Errorf("listening on port %d: %w", cfg.Port, err)
			}
			internalLn, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.InternalPort))
			if err != nil {
				return fmt.Errorf("listening on internal port %d: %w", cfg.InternalPort, err)
			}

			return runServers(ctx, logger, srv, internalSrv, ln, internalLn)
		},
	}
}

const shutdownTimeout = 10 * time.Second

// runServers serves both HTTP servers until the context is cancelled, then
// shuts them down gracefully and waits for in-flight requests to drain
// before returning.
func runServers(
	ctx context.Context,
	logger *slog.Logger,
	srv, internalSrv *http.Server,
	ln, internalLn net.Listener,
) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		logger.Info("shutting down HTTP servers")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = internalSrv.Shutdown(shutdownCtx)
	}()

	go func() {
		logger.Info("starting internal server", "addr", internalLn.Addr().String())
		if err := internalSrv.Serve(internalLn); err != nil && err != http.ErrServerClosed {
			logger.Error("internal server failed", "error", err)
		}
	}()

	logger.Info("starting server", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}

	// Serve only returns ErrServerClosed after Shutdown was initiated, so
	// wait for the shutdown goroutine to finish draining both servers.
	<-shutdownDone
	return nil
}

func reapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reap",
		Short: "Run a single reap cycle (for CronJob or debugging)",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(envStr("LOG_FORMAT", "json"))
			cfg := newConfig(logger)
			if err := cfg.Validate(); err != nil {
				return err
			}

			rdb, err := redisclient.New(cfg.RedisURL)
			if err != nil {
				return fmt.Errorf("connecting to redis: %w", err)
			}
			defer func() { _ = rdb.Close() }()

			ctx := context.Background()
			r := reaper.New(rdb, cfg.RegistryURL, logger.With("component", "reaper"))
			return r.ReapOnce(ctx)
		},
	}
}

func recoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Re-populate Redis by scanning the registry catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(envStr("LOG_FORMAT", "json"))
			cfg := newConfig(logger)
			if err := cfg.Validate(); err != nil {
				return err
			}

			rdb, err := redisclient.New(cfg.RedisURL)
			if err != nil {
				return fmt.Errorf("connecting to redis: %w", err)
			}
			defer func() { _ = rdb.Close() }()

			ctx := context.Background()
			reg := registry.New(cfg.RegistryURL)
			rec := recoverlib.New(rdb, reg, cfg.DefaultTTL, cfg.MaxTTL, logger.With("component", "recover"))

			if err := rec.Run(ctx); err != nil {
				return err
			}

			return rdb.SetInitialized(ctx)
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("ephemeron %s (commit: %s)\n", version, commit)
		},
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(logger *slog.Logger, key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		logger.Warn("invalid integer in environment variable, using fallback",
			"key", key, "value", v, "fallback", fallback)
		return fallback
	}
	return n
}

func envDuration(logger *slog.Logger, key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		logger.Warn("invalid duration in environment variable, using fallback",
			"key", key, "value", v, "fallback", fallback.String())
		return fallback
	}
	return d
}

func envStrSlice(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var result []string
	for _, s := range strings.Split(v, ",") {
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
