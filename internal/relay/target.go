package relay

import (
	"fmt"
	"net/url"
	"time"
)

// TargetConfig is the input form, populated from YAML/env.
type TargetConfig struct {
	Name              string        `koanf:"name"`
	URL               string        `koanf:"url"`
	Timeout           time.Duration `koanf:"timeout"`
	Retries           int           `koanf:"retries"`
	AllowHTTP         bool          `koanf:"allow_http"`
	AllowPrivateIPs   bool          `koanf:"allow_private_ips"`
	PreserveUserAgent bool          `koanf:"preserve_user_agent"`
}

// Target is the validated, parsed form used at request time.
type Target struct {
	Name              string
	BaseURL           *url.URL
	Timeout           time.Duration
	Retries           int
	AllowHTTP         bool
	AllowPrivateIPs   bool
	PreserveUserAgent bool
}

type TargetRegistry struct {
	targets map[string]Target
}

// NewTargetRegistry validates every target at startup: parses the URL, enforces
// scheme, and fails fast on misconfiguration so nothing sneaks through at request time.
func NewTargetRegistry(targets []TargetConfig) (*TargetRegistry, error) {
	m := make(map[string]Target, len(targets))
	for _, t := range targets {
		if t.Name == "" {
			return nil, fmt.Errorf("target missing name")
		}
		if t.URL == "" {
			return nil, fmt.Errorf("target %q missing URL", t.Name)
		}
		u, err := url.Parse(t.URL)
		if err != nil {
			return nil, fmt.Errorf("target %q: invalid URL: %w", t.Name, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("target %q: scheme %q not supported (use http or https)", t.Name, u.Scheme)
		}
		if u.Scheme == "http" && !t.AllowHTTP {
			return nil, fmt.Errorf("target %q: http:// rejected (set allow_http: true to override)", t.Name)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("target %q: URL has no host", t.Name)
		}
		if u.User != nil {
			return nil, fmt.Errorf("target %q: URL must not contain userinfo", t.Name)
		}

		timeout := t.Timeout
		if timeout == 0 {
			timeout = 5 * time.Second
		}
		retries := t.Retries
		if retries == 0 {
			retries = 3
		}

		m[t.Name] = Target{
			Name:              t.Name,
			BaseURL:           u,
			Timeout:           timeout,
			Retries:           retries,
			AllowHTTP:         t.AllowHTTP,
			AllowPrivateIPs:   t.AllowPrivateIPs,
			PreserveUserAgent: t.PreserveUserAgent,
		}
	}
	return &TargetRegistry{targets: m}, nil
}

func (r *TargetRegistry) Get(name string) (Target, error) {
	t, ok := r.targets[name]
	if !ok {
		return Target{}, fmt.Errorf("target %q not found", name)
	}
	return t, nil
}

func (r *TargetRegistry) List() []Target {
	out := make([]Target, 0, len(r.targets))
	for _, t := range r.targets {
		out = append(out, t)
	}
	return out
}
