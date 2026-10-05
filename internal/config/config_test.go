package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadSecretHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
relay:
  targets:
    - name: anthropic
      url: https://api.anthropic.com
      secret_headers:
        X-Api-Key: ANTHROPIC_API_KEY
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Relay.Targets) != 1 {
		t.Fatalf("targets: %+v", cfg.Relay.Targets)
	}
	got := cfg.Relay.Targets[0].SecretHeaders
	// koanf may lower-case map keys; header names are case-insensitive, so accept either.
	v, ok := got["X-Api-Key"]
	if !ok {
		v = got["x-api-key"]
	}
	if v != "ANTHROPIC_API_KEY" {
		t.Fatalf("secret_headers not loaded: %+v", got)
	}
}

func TestRateLimitDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rl := cfg.RateLimit
	if rl.Backend != "redis" || rl.FailOpen || rl.Requests != 100 || rl.Window != time.Minute {
		t.Fatalf("rate_limit defaults = %+v, want redis, fail-closed, 100/1m", rl)
	}
}

func TestEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		env, val string
		got      func(*Config) any
		want     any
	}{
		// "__" separates levels, so keys with underscores are reachable.
		{"RELAYER_RATE_LIMIT__FAIL_OPEN", "true", func(c *Config) any { return c.RateLimit.FailOpen }, true},
		{"RELAYER_RATE_LIMIT__BACKEND", "memory", func(c *Config) any { return c.RateLimit.Backend }, "memory"},
		{"RELAYER_RELAY__MAX_REQUEST_BYTES", "123", func(c *Config) any { return c.Relay.MaxRequestBytes }, int64(123)},
		{"RELAYER_QUEUE__COMPLETED_TTL", "5m", func(c *Config) any { return c.Queue.CompletedTTL }, 5 * time.Minute},
		{"RELAYER_QUEUE__REDIS__TLS", "true", func(c *Config) any { return c.Queue.Redis.TLS }, true},
		{"RELAYER_SERVER__PORT", "9001", func(c *Config) any { return c.Server.Port }, 9001},
		// Without "__", every "_" separates levels: keys without underscores keep working.
		{"RELAYER_SERVER_PORT", "9002", func(c *Config) any { return c.Server.Port }, 9002},
		{"RELAYER_QUEUE_REDIS_ADDR", "redis:6379", func(c *Config) any { return c.Queue.Redis.Addr }, "redis:6379"},
		// ...and can't reach a key with an underscore (rate.limit.fail.open), so the default stays.
		{"RELAYER_RATE_LIMIT_FAIL_OPEN", "true", func(c *Config) any { return c.RateLimit.FailOpen }, false},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv(tt.env, tt.val)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.got(cfg); got != tt.want {
				t.Fatalf("%s=%s: got %v (%T), want %v (%T)", tt.env, tt.val, got, got, tt.want, tt.want)
			}
		})
	}
}
