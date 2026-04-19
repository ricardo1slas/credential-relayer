package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// HandlerConfig is what wires together the security-sensitive knobs that live outside
// any single target (body caps, outbound User-Agent, redirect policy, header stripping).
type HandlerConfig struct {
	MaxRequestBytes        int64
	MaxResponseBytes       int64
	PreserveClientIdentity bool
	UserAgent              string
	MaxRedirects           int
}

func (c HandlerConfig) withDefaults() HandlerConfig {
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 1 << 20
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 10 << 20
	}
	if c.UserAgent == "" {
		c.UserAgent = "private-relayer/0.1"
	}
	if c.MaxRedirects < 0 {
		c.MaxRedirects = 0
	}
	return c
}

type RelayRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type RelayResponse struct {
	Status       string          `json:"status"`
	Target       string          `json:"target"`
	ResponseCode int             `json:"response_code,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	Error        string          `json:"error,omitempty"`
	Attempts     int             `json:"attempts"`
	Latency      string          `json:"latency"`
}

type Handler struct {
	registry *TargetRegistry
	cfg      HandlerConfig
	client   *http.Client
	logger   zerolog.Logger
}

// allowedMethods is the small, explicit set of HTTP methods we relay. Anything else
// (CONNECT, TRACE, custom) is rejected with 400 -- reduces abuse surface and aligns
// the relay with how most APIs are actually used.
var allowedMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

// deniedRequestHeaders are stripped from the client payload before forwarding to the target.
// Two buckets: hop-by-hop (RFC 7230 §6.1 -- meaningless to forward) and identity/auth
// (only the relay, not the client, gets to set these when talking to the target).
var deniedRequestHeaders = map[string]struct{}{
	"authorization":       {},
	"cookie":              {},
	"proxy-authorization": {},
	"proxy-authenticate":  {},
	"host":                {},
	"content-length":      {},
	"transfer-encoding":   {},
	"connection":          {},
	"keep-alive":          {},
	"upgrade":             {},
	"te":                  {},
	"trailer":             {},
	"x-forwarded-for":     {},
	"x-forwarded-proto":   {},
	"x-forwarded-host":    {},
	"x-real-ip":           {},
}

// identityHeaders are stripped when preserve_client_identity is false (default).
// They're not security-critical but leak caller fingerprinting to the target.
var identityHeaders = map[string]struct{}{
	"user-agent": {},
	"referer":    {},
	"forwarded":  {},
	"via":        {},
}

func NewHandler(registry *TargetRegistry, cfg HandlerConfig, logger zerolog.Logger) *Handler {
	cfg = cfg.withDefaults()
	return &Handler{
		registry: registry,
		cfg:      cfg,
		client:   newSafeClient(cfg, logger),
		logger:   logger,
	}
}

// newSafeClient builds an http.Client with an SSRF-hardened transport:
// the DialContext resolves the target hostname, rejects dials to private CIDRs,
// and the redirect policy caps hops and re-runs validation on each new host.
func newSafeClient(cfg HandlerConfig, logger zerolog.Logger) *http.Client {
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			allowPrivate, _ := privateAllowedFromCtx(ctx)
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolving %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IPs for %s", host)
			}
			if !allowPrivate {
				for _, ip := range ips {
					if isPrivateIP(ip.IP) {
						logger.Warn().Str("host", host).Str("ip", ip.IP.String()).Msg("blocked dial to private address")
						return nil, fmt.Errorf("dialing %s blocked: resolves to private address %s", host, ip.IP)
					}
				}
			}
			return baseDialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= cfg.MaxRedirects {
				return fmt.Errorf("too many redirects (%d)", len(via))
			}
			// Only allow redirects to http/https; block file://, ftp://, etc.
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to scheme %q rejected", req.URL.Scheme)
			}
			return nil
		},
	}
}

// Context key for per-request allowPrivateIPs flag (set in doRelay, consumed in DialContext).
type allowPrivateCtxKey struct{}

func ctxWithAllowPrivate(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, allowPrivateCtxKey{}, allow)
}

func privateAllowedFromCtx(ctx context.Context) (bool, bool) {
	v, ok := ctx.Value(allowPrivateCtxKey{}).(bool)
	return v, ok
}

// isPrivateIP covers RFC1918, loopback, link-local, and IPv6 ULA/link-local --
// the addresses SSRF attackers typically want to reach (cloud metadata at
// 169.254.169.254, internal services at 10.x / 192.168.x, localhost, etc.).
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// 100.64.0.0/10 -- CGNAT, not covered by IsPrivate.
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Cap inbound body. MaxBytesReader emits http.MaxBytesError if the client sends more.
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxRequestBytes)

	var req RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status := http.StatusBadRequest
		if _, ok := err.(*http.MaxBytesError); ok {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, RelayResponse{
			Status:  "error",
			Error:   "invalid request body: " + err.Error(),
			Latency: time.Since(start).String(),
		})
		return
	}

	if req.Target == "" {
		writeJSON(w, http.StatusBadRequest, RelayResponse{
			Status:  "error",
			Error:   "target is required",
			Latency: time.Since(start).String(),
		})
		return
	}

	if req.Method == "" {
		req.Method = http.MethodPost
	}
	req.Method = strings.ToUpper(req.Method)
	if _, ok := allowedMethods[req.Method]; !ok {
		writeJSON(w, http.StatusBadRequest, RelayResponse{
			Status:  "error",
			Target:  req.Target,
			Error:   fmt.Sprintf("method %q not allowed", req.Method),
			Latency: time.Since(start).String(),
		})
		return
	}

	target, err := h.registry.Get(req.Target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, RelayResponse{
			Status:  "error",
			Target:  req.Target,
			Error:   err.Error(),
			Latency: time.Since(start).String(),
		})
		return
	}

	resp, attempts, err := h.relayWithRetries(r.Context(), target, req)
	if err != nil {
		h.logger.Error().
			Err(err).
			Str("target", req.Target).
			Int("attempts", attempts).
			Msg("relay failed")

		writeJSON(w, http.StatusBadGateway, RelayResponse{
			Status:   "failed",
			Target:   req.Target,
			Error:    err.Error(),
			Attempts: attempts,
			Latency:  time.Since(start).String(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap response body -- a malicious target can't OOM us by returning gigabytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.cfg.MaxResponseBytes))
	if err != nil {
		h.logger.Warn().Err(err).Str("target", req.Target).Msg("reading response body")
	}

	h.logger.Info().
		Str("target", req.Target).
		Int("status", resp.StatusCode).
		Int("attempts", attempts).
		Int("response_bytes", len(body)).
		Dur("latency", time.Since(start)).
		Msg("relay succeeded")

	writeJSON(w, http.StatusOK, RelayResponse{
		Status:       "relayed",
		Target:       req.Target,
		ResponseCode: resp.StatusCode,
		ResponseBody: body,
		Attempts:     attempts,
		Latency:      time.Since(start).String(),
	})
}

// ExecuteRelay performs a relay to a named target. Used by both the HTTP handler and the async worker pool.
func (h *Handler) ExecuteRelay(ctx context.Context, targetName, method, path string, headers map[string]string, body json.RawMessage) (*http.Response, int, error) {
	target, err := h.registry.Get(targetName)
	if err != nil {
		return nil, 0, err
	}
	method = strings.ToUpper(method)
	if method == "" {
		method = http.MethodPost
	}
	if _, ok := allowedMethods[method]; !ok {
		return nil, 0, fmt.Errorf("method %q not allowed", method)
	}

	req := RelayRequest{
		Target:  targetName,
		Method:  method,
		Path:    path,
		Headers: headers,
		Body:    body,
	}

	return h.relayWithRetries(ctx, target, req)
}

func (h *Handler) relayWithRetries(ctx context.Context, target Target, req RelayRequest) (*http.Response, int, error) {
	var lastErr error

	for attempt := 0; attempt <= target.Retries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt - 1)
			h.logger.Warn().
				Str("target", target.Name).
				Int("attempt", attempt+1).
				Dur("backoff", delay).
				Err(lastErr).
				Msg("retrying relay")

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, attempt, fmt.Errorf("request cancelled: %w", ctx.Err())
			}
		}

		resp, err := h.doRelay(ctx, target, req)
		if err != nil {
			lastErr = err
			continue
		}

		// Don't retry on client errors (4xx) -- only on server errors (5xx) and transport failures.
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("target returned %d", resp.StatusCode)
			continue
		}

		return resp, attempt + 1, nil
	}

	return nil, target.Retries + 1, fmt.Errorf("all %d attempts failed: %w", target.Retries+1, lastErr)
}

func (h *Handler) doRelay(ctx context.Context, target Target, req RelayRequest) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, target.Timeout)
	defer cancel()

	// Pass the per-target allow_private_ips flag down to DialContext via context.
	ctx = ctxWithAllowPrivate(ctx, target.AllowPrivateIPs)

	finalURL, err := buildTargetURL(target.BaseURL, req.Path)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, finalURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	// Baseline headers the relay always controls.
	httpReq.Header.Set("Content-Type", "application/json")
	if !target.PreserveUserAgent && !h.cfg.PreserveClientIdentity {
		httpReq.Header.Set("User-Agent", h.cfg.UserAgent)
	}

	// Apply client headers through the allow/deny filter.
	for k, v := range req.Headers {
		if !isHeaderAllowed(k, h.cfg.PreserveClientIdentity || target.PreserveUserAgent) {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	return h.client.Do(httpReq)
}

// buildTargetURL resolves a client-supplied path against the target's base URL without
// allowing the path to change scheme, host, or add userinfo. This is what stops
// "path=@evil.com/x" from routing to evil.com.
func buildTargetURL(base *url.URL, clientPath string) (string, error) {
	if clientPath == "" {
		return base.String(), nil
	}
	if !strings.HasPrefix(clientPath, "/") {
		return "", fmt.Errorf("path must start with /")
	}
	ref, err := url.Parse(clientPath)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	if ref.Scheme != "" || ref.Host != "" || ref.User != nil {
		return "", fmt.Errorf("path must not contain scheme, host, or userinfo")
	}
	resolved := base.ResolveReference(ref)
	if resolved.Host != base.Host || resolved.Scheme != base.Scheme {
		return "", fmt.Errorf("path resolves outside target host")
	}
	return resolved.String(), nil
}

// isHeaderAllowed decides whether to forward a client-supplied header.
// Denied: hop-by-hop, auth, and connection-affecting headers (always blocked).
// Identity headers (User-Agent, Referer, ...) are blocked unless preserveIdentity is true.
func isHeaderAllowed(name string, preserveIdentity bool) bool {
	lc := strings.ToLower(name)
	if _, blocked := deniedRequestHeaders[lc]; blocked {
		return false
	}
	if _, isIdentity := identityHeaders[lc]; isIdentity {
		return preserveIdentity
	}
	return true
}

// StripSensitiveHeaders returns a copy of the header map with known-sensitive headers
// removed. Used by the queue to scrub values before persisting a job in Redis.
func StripSensitiveHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return h
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		lc := strings.ToLower(k)
		if _, blocked := deniedRequestHeaders[lc]; blocked {
			continue
		}
		out[k] = v
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
