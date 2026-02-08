package config

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	base := func() Config {
		return Config{
			Port:         8000,
			RedisURL:     "redis://localhost:6379",
			HookToken:    "secret",
			RegistryURL:  "http://localhost:5000",
			Hostname:     "reg.meh.wf",
			DefaultTTL:   time.Hour,
			MaxTTL:       24 * time.Hour,
			ReapInterval: time.Minute,
			LogFormat:    "text",
		}
	}

	t.Run("valid", func(t *testing.T) {
		c := base()
		if err := c.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("missing redis url", func(t *testing.T) {
		c := base()
		c.RedisURL = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for missing RedisURL")
		}
	})

	t.Run("missing hook token", func(t *testing.T) {
		c := base()
		c.HookToken = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for missing HookToken")
		}
	})

	t.Run("missing registry url", func(t *testing.T) {
		c := base()
		c.RegistryURL = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for missing RegistryURL")
		}
	})

	t.Run("default exceeds max", func(t *testing.T) {
		c := base()
		c.DefaultTTL = 48 * time.Hour
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when DefaultTTL > MaxTTL")
		}
	})

	t.Run("zero default ttl", func(t *testing.T) {
		c := base()
		c.DefaultTTL = 0
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for zero DefaultTTL")
		}
	})
}
