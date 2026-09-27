package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Server    ServerConfig    `koanf:"server"`
	Auth      AuthConfig      `koanf:"auth"`
	RateLimit RateLimitConfig `koanf:"rate_limit"`
	Relay     RelayConfig     `koanf:"relay"`
	Queue     QueueConfig     `koanf:"queue"`
	Metrics   MetricsConfig   `koanf:"metrics"`
}

type AuthConfig struct {
	APIKeys []APIKeyConfig `koanf:"api_keys"`
}

type APIKeyConfig struct {
	Name      string   `koanf:"name"`
	Key       string   `koanf:"key"`        // plaintext (dev only); prefer key_sha256
	KeySHA256 string   `koanf:"key_sha256"` // hex SHA-256 of the key (make new-key)
	ExpiresAt string   `koanf:"expires_at"` // optional RFC 3339 or YYYY-MM-DD
	Targets   []string `koanf:"targets"`    // allowed relay targets; empty or ["*"] = all
}

type RateLimitConfig struct {
	Requests int           `koanf:"requests"`
	Window   time.Duration `koanf:"window"`
}

type RelayConfig struct {
	Targets                []TargetConfig `koanf:"targets"`
	MaxRequestBytes        int64          `koanf:"max_request_bytes"`  // inbound body cap
	MaxResponseBytes       int64          `koanf:"max_response_bytes"` // target response cap
	PreserveClientIdentity bool           `koanf:"preserve_client_identity"`
	UserAgent              string         `koanf:"user_agent"`
	MaxRedirects           int            `koanf:"max_redirects"`
}

type TargetConfig struct {
	Name              string        `koanf:"name"`
	URL               string        `koanf:"url"`
	Timeout           time.Duration `koanf:"timeout"`
	Retries           int           `koanf:"retries"`
	AllowHTTP         bool          `koanf:"allow_http"`
	AllowPrivateIPs   bool          `koanf:"allow_private_ips"`
	PreserveUserAgent bool          `koanf:"preserve_user_agent"`
	// SecretHeaders maps header name -> env var NAME holding the value (injected by the relay).
	SecretHeaders map[string]string `koanf:"secret_headers"`
	AllowMethods  []string          `koanf:"allow_methods"`
	AllowPaths    []string          `koanf:"allow_paths"`
	MaxBodyBytes  int64             `koanf:"max_body_bytes"`
}

type ServerConfig struct {
	Port              int           `koanf:"port"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout"`
}

type QueueConfig struct {
	Redis        RedisConfig   `koanf:"redis"`
	Workers      int           `koanf:"workers"`
	RetryDelay   time.Duration `koanf:"retry_delay"`
	CompletedTTL time.Duration `koanf:"completed_ttl"`
	FailedTTL    time.Duration `koanf:"failed_ttl"`
}

type RedisConfig struct {
	Addr     string `koanf:"addr"`
	Password string `koanf:"password"`
	DB       int    `koanf:"db"`
	TLS      bool   `koanf:"tls"`
}

type MetricsConfig struct {
	Enabled bool `koanf:"enabled"`
	Port    int  `koanf:"port"`
	Pprof   bool `koanf:"pprof"` // expose /debug/pprof on the metrics port; off by default
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")

	// Load from YAML file.
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("loading config file: %w", err)
	}

	// Override with environment variables.
	// RELAYER_SERVER_PORT -> server.port
	if err := k.Load(env.Provider("RELAYER_", ".", func(s string) string {
		return strings.ReplaceAll(
			strings.ToLower(strings.TrimPrefix(s, "RELAYER_")),
			"_", ".",
		)
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env config: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	setDefaults(&cfg)
	return &cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 10 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 10 * time.Second
	}
	if cfg.Server.ReadHeaderTimeout == 0 {
		cfg.Server.ReadHeaderTimeout = 5 * time.Second
	}
	if cfg.Queue.Redis.Addr == "" {
		cfg.Queue.Redis.Addr = "localhost:6379"
	}
	if cfg.Metrics.Port == 0 {
		cfg.Metrics.Port = 9090
	}
	if cfg.Queue.Workers == 0 {
		cfg.Queue.Workers = 5
	}
	if cfg.Queue.RetryDelay == 0 {
		cfg.Queue.RetryDelay = 30 * time.Second
	}
	if cfg.Queue.CompletedTTL == 0 {
		cfg.Queue.CompletedTTL = time.Hour
	}
	if cfg.Queue.FailedTTL == 0 {
		cfg.Queue.FailedTTL = 24 * time.Hour
	}
	if cfg.RateLimit.Requests == 0 {
		cfg.RateLimit.Requests = 100
	}
	if cfg.RateLimit.Window == 0 {
		cfg.RateLimit.Window = time.Minute
	}
	if cfg.Relay.MaxRequestBytes == 0 {
		cfg.Relay.MaxRequestBytes = 1 << 20 // 1 MiB
	}
	if cfg.Relay.MaxResponseBytes == 0 {
		cfg.Relay.MaxResponseBytes = 10 << 20 // 10 MiB
	}
	if cfg.Relay.UserAgent == "" {
		cfg.Relay.UserAgent = "credential-relayer/0.1"
	}
	if cfg.Relay.MaxRedirects == 0 {
		cfg.Relay.MaxRedirects = 3
	}
}
