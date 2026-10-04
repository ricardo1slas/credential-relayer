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
