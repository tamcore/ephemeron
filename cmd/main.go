package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"github.com/tamcore/ephemeron/internal/config"
	"github.com/tamcore/ephemeron/internal/hooks"
	"github.com/tamcore/ephemeron/internal/reaper"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
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
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newConfig() *config.Config {
	return &config.Config{
		Port:         envInt("PORT", 8000),
		InternalPort: envInt("INTERNAL_PORT", 9090),
		RedisURL:     envStr("REDIS_URL", envStr("REDISCLOUD_URL", "redis://localhost:6379")),
		HookToken:    envStr("HOOK_TOKEN", ""),
		RegistryURL:  envStr("REGISTRY_URL", "http://localhost:5000"),
		Hostname:     envStr("HOSTNAME_OVERRIDE", "localhost"),
		DefaultTTL:   envDuration("DEFAULT_TTL", time.Hour),
		MaxTTL:       envDuration("MAX_TTL", 24*time.Hour),
		ReapInterval: envDuration("REAP_INTERVAL", time.Minute),
		LogFormat:    envStr("LOG_FORMAT", "json"),
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
			cfg := newConfig()
			if err := cfg.Validate(); err != nil {
				return err
			}

			logger := setupLogger(cfg.LogFormat)

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

			// Start reaper in background.
			r := reaper.New(rdb, cfg.RegistryURL, logger.With("component", "reaper"))
			go r.RunLoop(ctx, cfg.ReapInterval)

			// Set up public HTTP routes (webhook + landing page).
			mux := http.NewServeMux()

			hookHandler := hooks.NewHandler(
				rdb, cfg.HookToken, cfg.DefaultTTL, cfg.MaxTTL,
				logger.With("component", "hooks"),
			)
			mux.Handle("POST /v1/hook/registry-event", hookHandler)

			webHandler, err := web.NewHandler(cfg.Hostname, cfg.DefaultTTL, cfg.MaxTTL, logger.With("component", "web"))
			if err != nil {
				return fmt.Errorf("creating web handler: %w", err)
			}
			mux.Handle("GET /{$}", webHandler)

			// Set up internal HTTP routes (probes + metrics).
			internalMux := http.NewServeMux()
			internalMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
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
				Addr:              fmt.Sprintf(":%d", cfg.Port),
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			internalSrv := &http.Server{
				Addr:              fmt.Sprintf(":%d", cfg.InternalPort),
				Handler:           internalMux,
				ReadHeaderTimeout: 5 * time.Second,
			}

			go func() {
				<-ctx.Done()
				logger.Info("shutting down HTTP servers")
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				_ = srv.Shutdown(shutdownCtx)
				_ = internalSrv.Shutdown(shutdownCtx)
			}()

			go func() {
				logger.Info("starting internal server", "port", cfg.InternalPort)
				if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Error("internal server failed", "error", err)
				}
			}()

			logger.Info("starting server", "port", cfg.Port)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}
}

func reapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reap",
		Short: "Run a single reap cycle (for CronJob or debugging)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := newConfig()
			if err := cfg.Validate(); err != nil {
				return err
			}

			logger := setupLogger(cfg.LogFormat)

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

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
