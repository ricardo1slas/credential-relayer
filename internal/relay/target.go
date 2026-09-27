package relay

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
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
	// Request policy. Empty means no restriction beyond the global rules.
	AllowMethods []string `koanf:"allow_methods"`  // subset of GET/POST/PUT/PATCH/DELETE
	AllowPaths   []string `koanf:"allow_paths"`    // path prefixes on segment boundaries: "/v1/messages"
	MaxBodyBytes int64    `koanf:"max_body_bytes"` // cap on the relayed body, below the global max_request_bytes
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

	allowMethods []string
	allowPaths   []string
	maxBodyBytes int64
}

// RequestError is a request rejected before anything is sent upstream. Status is the HTTP
// code to return to the client.
type RequestError struct {
	Status int
	Msg    string
	Reason string // metric label: scope, bad_method, not_found, bad_path, method, path, body
}

func (e *RequestError) Error() string { return e.Msg }

// checkPolicy applies the target's request policy. resolvedPath is the escaped path after
// buildTargetURL's resolution (dot segments already removed), never the raw client path,
// so "/v1/messages/../admin" is judged as "/admin".
func (t Target) checkPolicy(method, resolvedPath string, bodyLen int) error {
	if len(t.allowMethods) > 0 && !slices.Contains(t.allowMethods, method) {
		return &RequestError{Status: http.StatusForbidden, Msg: fmt.Sprintf("method %s not allowed for this target", method), Reason: "method"}
	}
	if len(t.allowPaths) > 0 && !t.pathAllowed(resolvedPath) {
		return &RequestError{Status: http.StatusForbidden, Msg: "path not allowed for this target", Reason: "path"}
	}
	if t.maxBodyBytes > 0 && int64(bodyLen) > t.maxBodyBytes {
		return &RequestError{Status: http.StatusRequestEntityTooLarge, Msg: fmt.Sprintf("body exceeds %d bytes for this target", t.maxBodyBytes), Reason: "body"}
	}
	return nil
}

// pathAllowed matches prefixes on segment boundaries ("/v1/messages" allows "/v1/messages" and
// "/v1/messages/x", not "/v1/messagesX"). Paths an upstream might decode or normalize
// differently than we do are refused outright: encoded dots/slashes/backslashes ("%2e%2e",
// "%2f"), raw backslashes, and ";" (Tomcat-style "..;/" traversal).
func (t Target) pathAllowed(p string) bool {
	if p == "" {
		p = "/"
	}
	lp := strings.ToLower(p)
	if strings.Contains(lp, "%2e") || strings.Contains(lp, "%2f") || strings.Contains(lp, "%5c") ||
		strings.ContainsAny(p, `\;`) {
		return false
	}
	for _, prefix := range t.allowPaths {
		if p == prefix || strings.HasPrefix(p, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

// normalizePolicy validates a target's policy config at startup.
func normalizePolicy(tc TargetConfig) (methods, paths []string, err error) {
	for _, m := range tc.AllowMethods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if _, ok := allowedMethods[m]; !ok {
			return nil, nil, fmt.Errorf("target %q: allow_methods: %q is not one of GET, POST, PUT, PATCH, DELETE", tc.Name, m)
		}
		methods = append(methods, m)
	}
	for _, p := range tc.AllowPaths {
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, `\;?#%`) {
			return nil, nil, fmt.Errorf("target %q: allow_paths: %q must be a plain absolute path like /v1/messages", tc.Name, p)
		}
		paths = append(paths, p)
	}
	if tc.MaxBodyBytes < 0 {
		return nil, nil, fmt.Errorf("target %q: max_body_bytes must be >= 0", tc.Name)
	}
	return methods, paths, nil
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

// worstCase bounds a /relay call: every attempt timing out, plus the longest backoffs.
func (t Target) worstCase() time.Duration {
	d := time.Duration(t.Retries+1) * t.Timeout
	for i := 0; i < t.Retries; i++ {
		b := maxDelay
		if i < 6 { // 500ms<<6 already exceeds maxDelay; also avoids shift overflow
			b = min(baseDelay<<i, maxDelay)
		}
		d += b
	}
	return d
}

// setSecrets injects the target's credentials. Call it after client headers are applied:
// the relay, not the client, owns these.
func (t Target) setSecrets(h http.Header) {
	for _, s := range t.secrets {
		h.Set(s.name, s.value)
	}
}

// requestCtx carries per-target transport policy to the dialer (allow_private_ips) and to
// CheckRedirect (which secrets to strip on a cross-origin hop).
func (t Target) requestCtx(ctx context.Context) context.Context {
	ctx = ctxWithAllowPrivate(ctx, t.AllowPrivateIPs)
	if len(t.secrets) > 0 {
		ctx = context.WithValue(ctx, secretsCtxKey{}, t.secrets)
	}
	return ctx
}

// redact replaces every injected secret value in b, so an upstream that echoes request
// headers (httpbin /headers, debug endpoints) can't hand the secret back to the client.
// ponytail: literal match only; a secret re-encoded by the upstream (base64, URL-escaped) survives.
// The /proxy stream uses streamRedactor, which also catches secrets split across chunks.
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
		methods, paths, err := normalizePolicy(t)
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
			allowMethods:      methods,
			allowPaths:        paths,
			maxBodyBytes:      t.MaxBodyBytes,
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
