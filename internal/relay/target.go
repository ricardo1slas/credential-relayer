package relay

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	// SecretHeaders maps an outbound header name to the NAME of the environment variable
	// holding its value (e.g. {"X-Api-Key": "ANTHROPIC_API_KEY"}). Values never live in YAML.
	SecretHeaders map[string]string `koanf:"secret_headers"`
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
	// secrets is unexported so the resolved values can never be marshaled into a response or log.
	secrets []secretHeader
}

type secretHeader struct {
	name   string // canonical header name
	folded string // foldHeaderName(name), for matching client headers
	value  string
}

// framingHeaders can't carry a secret: setting them would break or smuggle the request itself.
var framingHeaders = map[string]struct{}{
	"host": {}, "content-length": {}, "transfer-encoding": {}, "connection": {},
	"keep-alive": {}, "upgrade": {}, "te": {}, "trailer": {}, "content-type": {},
}

// resolveSecrets reads each secret header's value from the environment. It fails fast on a
// missing or unusable variable, and never puts a value in an error message.
func resolveSecrets(target string, m map[string]string) ([]secretHeader, error) {
	out := make([]secretHeader, 0, len(m))
	for name, envVar := range m {
		folded := foldHeaderName(name)
		if folded == "" || strings.ContainsAny(folded, " \t\r\n:") {
			return nil, fmt.Errorf("target %q: invalid secret header name %q", target, name)
		}
		if _, bad := framingHeaders[folded]; bad {
			return nil, fmt.Errorf("target %q: header %q cannot be a secret header", target, name)
		}
		v, ok := os.LookupEnv(envVar)
		if !ok || v == "" {
			return nil, fmt.Errorf("target %q: secret header %q: env var %q is unset or empty", target, name, envVar)
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("target %q: secret header %q: env var %q contains a newline", target, name, envVar)
		}
		out = append(out, secretHeader{name: http.CanonicalHeaderKey(name), folded: folded, value: v})
	}
	return out, nil
}

// isSecretHeader reports whether a client-supplied header name collides with one the relay injects.
func (t Target) isSecretHeader(name string) bool {
	f := foldHeaderName(name)
	for _, s := range t.secrets {
		if s.folded == f {
			return true
		}
	}
	return false
}

// redact replaces every injected secret value in b, so an upstream that echoes request
// headers (httpbin /headers, debug endpoints) can't hand the secret back to the client.
// ponytail: literal match only; a secret re-encoded by the upstream (base64, URL-escaped) survives.
func (t Target) redact(b []byte) []byte {
	for _, s := range t.secrets {
		if bytes.Contains(b, []byte(s.value)) {
			b = bytes.ReplaceAll(b, []byte(s.value), []byte("[REDACTED]"))
		}
	}
	return b
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

		secrets, err := resolveSecrets(t.Name, t.SecretHeaders)
		if err != nil {
			return nil, err
		}

		m[t.Name] = Target{
			Name:              t.Name,
			BaseURL:           u,
			Timeout:           timeout,
			Retries:           retries,
			AllowHTTP:         t.AllowHTTP,
			AllowPrivateIPs:   t.AllowPrivateIPs,
			PreserveUserAgent: t.PreserveUserAgent,
			secrets:           secrets,
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
