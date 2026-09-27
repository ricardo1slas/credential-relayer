package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ricardo1slas/credential-relayer/internal/auth"
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
		c.UserAgent = "credential-relayer/0.1"
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
	metrics  Recorder
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
		metrics:  nopRecorder{},
	}
}

// SetMetrics attaches a metrics recorder. Passing nil keeps the no-op default.
func (h *Handler) SetMetrics(r Recorder) {
	if r != nil {
		h.metrics = r
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
		// Idle pool must cover peak concurrency per upstream, else every burst re-dials
		// (profiled: 80% of CPU in dialConn at c=200 with the old limit of 10).
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 256,
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
			// net/http copies every header onto the redirected request and only strips
			// Authorization/Cookie on a host change. Injected secrets go to the configured
			// origin only: drop them on any host or scheme change (incl. an https->http downgrade).
			if orig := via[0].URL; req.URL.Host != orig.Host || req.URL.Scheme != orig.Scheme {
				secrets, _ := req.Context().Value(secretsCtxKey{}).([]secretHeader)
				for _, s := range secrets {
					req.Header.Del(s.name)
				}
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

// Context key carrying the target's injected secrets to CheckRedirect.
type secretsCtxKey struct{}

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

	req.Method = NormalizeMethod(req.Method)
	target, err := h.Authorize(r.Context(), req.Target, req.Method, req.Path, len(req.Body))
	if err != nil {
		var rerr *RequestError
		if !errors.As(err, &rerr) {
			rerr = &RequestError{Status: http.StatusInternalServerError, Msg: "internal error"}
		}
		h.logger.Warn().Str("key", auth.KeyName(r.Context())).Str("target", req.Target).Str("method", req.Method).
			Int("status", rerr.Status).Msg("relay rejected: " + rerr.Msg)
		writeJSON(w, rerr.Status, RelayResponse{
			Status:  "error",
			Target:  req.Target,
			Error:   rerr.Msg,
			Latency: time.Since(start).String(),
		})
		return
	}

	// The server's WriteTimeout is sized for quick calls; give this response the target's worst
	// case (every attempt timing out, plus backoff) so a slow upstream isn't cut off mid-reply.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(target.worstCase() + writeGrace))

	resp, attempts, err := h.relayWithRetries(r.Context(), target, req)
	if err != nil {
		h.logger.Error().
			Err(err).
			Str("key", auth.KeyName(r.Context())).
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
	// A body cut at the cap mid-secret is also cut mid-JSON-string, which RawMessage refuses to
	// encode, so a partial secret can't reach the client here (unlike the raw /proxy stream).
	body = target.redact(body)

	h.logger.Info().
		Str("key", auth.KeyName(r.Context())).
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
// Policy is re-checked here: a job queued before a config change must not bypass the new policy.
func (h *Handler) ExecuteRelay(ctx context.Context, targetName, method, path string, headers map[string]string, body json.RawMessage) (*http.Response, int, error) {
	method = NormalizeMethod(method)
	target, err := h.Authorize(ctx, targetName, method, path, len(body))
	if err != nil {
		return nil, 0, err
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

// NormalizeMethod defaults an empty method to POST and upper-cases it.
func NormalizeMethod(m string) string {
	if m == "" {
		return http.MethodPost
	}
	return strings.ToUpper(m)
}

// Authorize is the single request gate for /relay, /webhook (at enqueue, so a bad job is
// refused with a 4xx instead of dead-lettered), /proxy and the worker: the caller's key scope
// (from ctx), global method allow-list, target lookup, path validation, then the target's own
// policy. Errors are *RequestError, and every rejection is counted by reason. Scope is checked
// first so a key can't tell a target it may not use from one that doesn't exist.
func (h *Handler) Authorize(ctx context.Context, targetName, method, path string, bodyLen int) (Target, error) {
	target, err := h.authorize(ctx, targetName, method, path, bodyLen)
	var rerr *RequestError
	if errors.As(err, &rerr) {
		label := "unknown" // client-chosen names must never become metric label values
		if _, gerr := h.registry.Get(targetName); gerr == nil {
			label = targetName
		}
		h.metrics.RecordRejection(label, auth.KeyName(ctx), rerr.Reason)
	}
	return target, err
}

func (h *Handler) authorize(ctx context.Context, targetName, method, path string, bodyLen int) (Target, error) {
	if !auth.TargetAllowed(ctx, targetName) {
		return Target{}, &RequestError{Status: http.StatusForbidden, Msg: "key not permitted for this target", Reason: "scope"}
	}
	if _, ok := allowedMethods[method]; !ok {
		return Target{}, &RequestError{Status: http.StatusBadRequest, Msg: fmt.Sprintf("method %q not allowed", method), Reason: "bad_method"}
	}
	target, err := h.registry.Get(targetName)
	if err != nil {
		return Target{}, &RequestError{Status: http.StatusNotFound, Msg: err.Error(), Reason: "not_found"}
	}
	resolved, err := buildTargetURL(target.BaseURL, path)
	if err != nil {
		return Target{}, &RequestError{Status: http.StatusBadRequest, Msg: "invalid path: " + err.Error(), Reason: "bad_path"}
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return Target{}, &RequestError{Status: http.StatusBadRequest, Msg: "invalid path", Reason: "bad_path"}
	}
	if err := target.checkPolicy(method, u.EscapedPath(), bodyLen); err != nil {
		return Target{}, err
	}
	return target, nil
}

func (h *Handler) relayWithRetries(ctx context.Context, target Target, req RelayRequest) (*http.Response, int, error) {
	var lastErr error
	start := time.Now()

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

		h.metrics.RecordRelay(target.Name, auth.KeyName(ctx), resp.StatusCode, attempt+1, false, time.Since(start))
		return resp, attempt + 1, nil
	}

	h.metrics.RecordRelay(target.Name, auth.KeyName(ctx), 0, target.Retries+1, true, time.Since(start))
	return nil, target.Retries + 1, fmt.Errorf("all %d attempts failed: %w", target.Retries+1, lastErr)
}

func (h *Handler) doRelay(ctx context.Context, target Target, req RelayRequest) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, target.Timeout)

	ctx = target.requestCtx(ctx)

	finalURL, err := buildTargetURL(target.BaseURL, req.Path)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("building URL: %w", err)
	}

	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, finalURL, bodyReader)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("building request: %w", err)
	}

	// Baseline headers the relay always controls.
	httpReq.Header.Set("Content-Type", "application/json")
	if !target.PreserveUserAgent && !h.cfg.PreserveClientIdentity {
		httpReq.Header.Set("User-Agent", h.cfg.UserAgent)
	}

	// Apply client headers through the allow/deny filter.
	// Client headers colliding with a secret header (in any case/underscore spelling) are
	// dropped, so the client can neither override the secret nor send a second copy of it.
	for k, v := range req.Headers {
		if !isHeaderAllowed(k, h.cfg.PreserveClientIdentity || target.PreserveUserAgent) || target.isSecretHeader(k) {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	target.setSecrets(httpReq.Header)

	resp, err := h.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	// The caller reads the body after we return, so the timeout context must live until
	// Close. Cancelling earlier makes net/http drop the connection instead of reusing it.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
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
// foldHeaderName lower-cases and folds "_" to "-" (and trims spaces). CGI/WSGI-style backends
// read X_Real_IP as X-Real-IP, so the deny-list must match the folded form or a client can
// smuggle a denied header past it. Found by FuzzHeaderFilter.
func foldHeaderName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

func isHeaderAllowed(name string, preserveIdentity bool) bool {
	lc := foldHeaderName(name)
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
		lc := foldHeaderName(k)
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
