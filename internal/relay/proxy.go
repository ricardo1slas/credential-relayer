package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ricardo1slas/private_relayer/internal/auth"
)

// writeGrace lets a 502 for a timed-out upstream still be written after a lifted write deadline.
const writeGrace = 5 * time.Second

// proxyDropRequest are client headers the proxy never forwards, on top of the /relay deny-list.
var proxyDropRequest = map[string]struct{}{
	// The caller's relayer key, in either header auth accepts: x-api-key (Anthropic SDK,
	// ANTHROPIC_API_KEY=<relayer key>) or x-goog-api-key (Google SDKs, GEMINI_API_KEY=<relayer
	// key>). It must never reach a third party. Upstream credentials come from secret_headers.
	"x-api-key":      {},
	"x-goog-api-key": {},
	// Let the transport negotiate gzip and decompress, so redaction sees plaintext.
	"accept-encoding": {},
}

// proxyDropResponse are upstream headers not passed back: hop-by-hop, a Content-Length that
// redaction may invalidate, and cookies (a session minted for the relay's credential).
var proxyDropResponse = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "transfer-encoding": {}, "upgrade": {}, "trailer": {}, "te": {},
	"proxy-authenticate": {}, "proxy-connection": {}, "content-length": {}, "set-cookie": {},
}

// Proxy forwards a raw HTTP request to a target and streams the response back, for SDKs that
// only let you change the base URL (ANTHROPIC_BASE_URL=http://relayer/api/v1/proxy/anthropic).
// Same gate as /relay (key scope, target policy, secret injection, SSRF-hardened client) but no
// JSON envelope and no retries: a streamed response can't be replayed, and LLM POSTs aren't
// idempotent. path is the escaped path after the target name, as the client sent it.
func (h *Handler) Proxy(w http.ResponseWriter, r *http.Request, targetName, path string) {
	start := time.Now()
	key := auth.KeyName(r.Context())
	fail := func(status int, msg string) {
		h.logger.Warn().Str("key", key).Str("target", targetName).Int("status", status).Msg("proxy rejected: " + msg)
		writeJSON(w, status, map[string]string{"error": msg, "target": targetName})
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxRequestBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			fail(http.StatusBadRequest, "reading request body")
		}
		return
	}
	target, err := h.Authorize(r.Context(), targetName, r.Method, path, len(body))
	if err != nil {
		var rerr *RequestError
		if !errors.As(err, &rerr) {
			rerr = &RequestError{Status: http.StatusInternalServerError, Msg: "internal error"}
		}
		fail(rerr.Status, rerr.Msg)
		return
	}

	// target.Timeout bounds the whole exchange, stream included (set it to the longest stream
	// you expect). The server's WriteTimeout is sized for /relay, so lift it for this response,
	// with a grace period so a 502 for a timed-out upstream can still be written.
	ctx, cancel := context.WithTimeout(target.requestCtx(r.Context()), target.Timeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(target.Timeout + writeGrace))

	finalURL, _ := buildTargetURL(target.BaseURL, path) // validated by Authorize
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	// #nosec G704 -- scheme+host are pinned to the configured target by buildTargetURL (fuzzed),
	// method and path passed Authorize, and the dialer refuses private addresses.
	out, err := http.NewRequestWithContext(ctx, r.Method, finalURL, reqBody)
	if err != nil {
		fail(http.StatusBadRequest, "building request")
		return
	}
	preserve := h.cfg.PreserveClientIdentity || target.PreserveUserAgent
	for k, vs := range r.Header {
		if _, drop := proxyDropRequest[foldHeaderName(k)]; drop || !isHeaderAllowed(k, preserve) || target.isSecretHeader(k) {
			continue
		}
		out.Header[k] = append([]string(nil), vs...)
	}
	if !preserve {
		out.Header.Set("User-Agent", h.cfg.UserAgent)
	}
	target.setSecrets(out.Header)

	resp, err := h.client.Do(out) // #nosec G704 -- see NewRequestWithContext above
	if err != nil {
		h.metrics.RecordRelay(target.Name, key, 0, 1, true, time.Since(start))
		h.logger.Error().Err(err).Str("key", key).Str("target", target.Name).Msg("proxy failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "target": targetName})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		if _, drop := proxyDropResponse[foldHeaderName(k)]; !drop {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, ttfb, truncated, err := h.stream(w, resp.Body, target, start)
	h.metrics.RecordRelay(target.Name, key, resp.StatusCode, 1, false, time.Since(start))
	h.metrics.RecordStream(target.Name, key, ttfb, time.Since(start), n)

	ev := h.logger.Info()
	if err != nil || truncated {
		ev = h.logger.Warn().Err(err).Bool("truncated", truncated)
	}
	ev.Str("key", key).Str("target", target.Name).Str("method", r.Method).Int("status", resp.StatusCode).
		Int64("response_bytes", n).Dur("latency", time.Since(start)).Msg("proxied")
}

// stream copies body to w, flushing after every read so SSE events reach the client as they
// arrive, redacting secrets across chunk boundaries, and capping at MaxResponseBytes. ttfb is
// the time from start to the first byte sent to the client (0 if none).
func (h *Handler) stream(w http.ResponseWriter, body io.Reader, t Target, start time.Time) (written int64, ttfb time.Duration, truncated bool, err error) {
	rc := http.NewResponseController(w)
	red := &streamRedactor{t: t}
	limited := &io.LimitedReader{R: body, N: h.cfg.MaxResponseBytes + 1} // +1 detects overflow
	buf := make([]byte, 32<<10)
	for {
		n, rerr := limited.Read(buf)
		if limited.N == 0 { // read past the cap: drop the extra byte and stop
			n--
			truncated = true
		}
		if n > 0 {
			m, werr := w.Write(red.push(buf[:n]))
			if m > 0 && ttfb == 0 {
				ttfb = time.Since(start)
			}
			written += int64(m)
			if werr != nil {
				return written, ttfb, truncated, werr
			}
			_ = rc.Flush()
		}
		if truncated {
			return written, ttfb, true, nil // held-back bytes may be a secret prefix: dropped
		}
		if rerr != nil {
			m, _ := w.Write(red.flush())
			if m > 0 && ttfb == 0 {
				ttfb = time.Since(start)
			}
			written += int64(m)
			if errors.Is(rerr, io.EOF) {
				rerr = nil
			}
			return written, ttfb, false, rerr
		}
	}
}

// streamRedactor redacts a target's secrets from a byte stream split at arbitrary points.
// It holds back only the longest tail that could still grow into a secret, so ordinary chunks
// (an SSE event rarely ends in "sk-ant-") pass through without delay.
type streamRedactor struct {
	t       Target
	pending []byte
}

func (s *streamRedactor) push(p []byte) []byte {
	if len(s.t.secrets) == 0 {
		return p
	}
	buf := s.t.redact(append(s.pending, p...))
	hold := 0
	for _, sec := range s.t.secrets {
		for k := min(len(sec.value)-1, len(buf)); k > hold; k-- {
			if bytes.HasSuffix(buf, []byte(sec.value[:k])) {
				hold = k
				break
			}
		}
	}
	s.pending = append([]byte(nil), buf[len(buf)-hold:]...)
	return buf[:len(buf)-hold]
}

// flush returns what push held back; call it once the stream ended normally.
func (s *streamRedactor) flush() []byte {
	p := s.pending
	s.pending = nil
	return p
}
