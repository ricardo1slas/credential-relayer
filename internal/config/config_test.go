package config

import (
	"os"
	"path/filepath"
	"testing"
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
