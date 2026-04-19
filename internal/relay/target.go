package relay

import (
	"fmt"
	"time"
)

type TargetConfig struct {
	Name    string        `koanf:"name"`
	URL     string        `koanf:"url"`
	Timeout time.Duration `koanf:"timeout"`
	Retries int           `koanf:"retries"`
}

type TargetRegistry struct {
	targets map[string]TargetConfig
}

func NewTargetRegistry(targets []TargetConfig) (*TargetRegistry, error) {
	m := make(map[string]TargetConfig, len(targets))
	for _, t := range targets {
		if t.Name == "" {
			return nil, fmt.Errorf("target missing name")
		}
		if t.URL == "" {
			return nil, fmt.Errorf("target %q missing URL", t.Name)
		}
		if t.Timeout == 0 {
			t.Timeout = 5 * time.Second
		}
		if t.Retries == 0 {
			t.Retries = 3
		}
		m[t.Name] = t
	}
	return &TargetRegistry{targets: m}, nil
}

func (r *TargetRegistry) Get(name string) (TargetConfig, error) {
	t, ok := r.targets[name]
	if !ok {
		return TargetConfig{}, fmt.Errorf("target %q not found", name)
	}
	return t, nil
}

func (r *TargetRegistry) List() []TargetConfig {
	out := make([]TargetConfig, 0, len(r.targets))
	for _, t := range r.targets {
		out = append(out, t)
	}
	return out
}
