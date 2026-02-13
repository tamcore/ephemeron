package config

import (
	"fmt"
	"time"
)

// Config holds all configuration for the application.
type Config struct {
	// Port for the public HTTP server (webhook + landing page).
	Port int

	// InternalPort for health/readiness probes and metrics (not publicly exposed).
	InternalPort int

	// RedisURL is the Redis connection URL.
	RedisURL string

	// HookToken is the shared secret for registry webhook authentication.
	HookToken string

	// RegistryURL is the base URL of the OCI registry (used by the reaper).
	RegistryURL string

	// Hostname is the public hostname for the landing page.
	Hostname string

	// DefaultTTL is the TTL applied when a tag has no parseable duration.
	DefaultTTL time.Duration

	// MaxTTL is the maximum allowed TTL.
	MaxTTL time.Duration

	// ReapInterval is how often the reaper checks for expired images.
	ReapInterval time.Duration

	// LogFormat controls log output: "json" or "text".
	LogFormat string

	// ImmutableTagPatterns are glob patterns for tags that cannot be overwritten.
	// Empty list = observability mode only (default). Example: ["prod-*", "release-*"]
	ImmutableTagPatterns []string
}

// Validate checks that all required configuration values are set.
func (c *Config) Validate() error {
	if c.RedisURL == "" {
		return fmt.Errorf("REDIS_URL is required")
	}
	if c.HookToken == "" {
		return fmt.Errorf("HOOK_TOKEN is required")
	}
	if c.RegistryURL == "" {
		return fmt.Errorf("REGISTRY_URL is required")
	}
	if c.DefaultTTL <= 0 {
		return fmt.Errorf("DEFAULT_TTL must be positive")
	}
	if c.MaxTTL <= 0 {
		return fmt.Errorf("MAX_TTL must be positive")
	}
	if c.DefaultTTL > c.MaxTTL {
		return fmt.Errorf("DEFAULT_TTL (%s) must not exceed MAX_TTL (%s)", c.DefaultTTL, c.MaxTTL)
	}
	return nil
}
