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
	Name string `koanf:"name"`
	Key  string `koanf:"key"`
}

type RateLimitConfig struct {
	Requests int           `koanf:"requests"`
	Window   time.Duration `koanf:"window"`
}

type RelayConfig struct {
	Targets []TargetConfig `koanf:"targets"`
}

type TargetConfig struct {
	Name    string        `koanf:"name"`
	URL     string        `koanf:"url"`
	Timeout time.Duration `koanf:"timeout"`
	Retries int           `koanf:"retries"`
}

type ServerConfig struct {
	Port         int           `koanf:"port"`
	ReadTimeout  time.Duration `koanf:"read_timeout"`
	WriteTimeout time.Duration `koanf:"write_timeout"`
}

type QueueConfig struct {
	Redis      RedisConfig   `koanf:"redis"`
	Workers    int           `koanf:"workers"`
	RetryDelay time.Duration `koanf:"retry_delay"`
}

type RedisConfig struct {
	Addr     string `koanf:"addr"`
	Password string `koanf:"password"`
	DB       int    `koanf:"db"`
}

type MetricsConfig struct {
	Enabled bool `koanf:"enabled"`
	Port    int  `koanf:"port"`
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
	if cfg.RateLimit.Requests == 0 {
		cfg.RateLimit.Requests = 100
	}
	if cfg.RateLimit.Window == 0 {
		cfg.RateLimit.Window = time.Minute
	}
}
