package relay

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/rs/zerolog"
)

// proxyServer mounts h.Proxy at /p/... behind a real server (a recorder can't prove streaming).
// scopes nil = unrestricted key.
func proxyServer(t *testing.T, h *Handler, target string, scopes []string, writeTimeout time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithKey(r.Context(), "test", scopes))
		h.Proxy(w, r, target, strings.TrimPrefix(r.URL.EscapedPath(), "/p"))
	}))
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func proxyHandlerFor(t *testing.T, cfg HandlerConfig, tc TargetConfig) *Handler {
	t.Helper()
	tc.AllowHTTP, tc.AllowPrivateIPs = true, true
	registry, err := NewTargetRegistry([]TargetConfig{tc})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(registry, cfg, zerolog.Nop())
}

func TestProxyStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	h := proxyHandlerFor(t, HandlerConfig{}, TargetConfig{Name: "llm", URL: upstream.URL, Timeout: 5 * time.Second})
	srv := proxyServer(t, h, "llm", nil, 0)

	resp, err := http.Post(srv.URL+"/p/v1/messages", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case got := <-first:
		if got != "data: one\n" {
			t.Fatalf("first line %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event not delivered before the upstream finished: proxy is buffering")
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if string(rest) != "\ndata: two\n\n" {
		t.Fatalf("rest %q", rest)
	}
}

func TestProxyHeadersSecretsAndRedaction(t *testing.T) {
	t.Setenv("RELAY_TEST_KEY", testSecret)
	var got http.Header
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotQuery = r.Header.Clone(), r.URL.RawQuery
		w.Header().Set("Set-Cookie", "session=abc")
		w.Header().Set("Request-Id", "req_123")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusCreated)
		// gzip-compressed, secret split across two flushes: redaction must see plaintext and
		// span chunk boundaries.
		gz := gzip.NewWriter(w)
		half := len(testSecret) / 2
		_, _ = io.WriteString(gz, `{"echo":"`+testSecret[:half])
		_ = gz.Flush()
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(gz, testSecret[half:]+`"}`)
		_ = gz.Close()
	}))
	defer upstream.Close()

	h := proxyHandlerFor(t, HandlerConfig{}, TargetConfig{
		Name: "llm", URL: upstream.URL, Timeout: 5 * time.Second,
		SecretHeaders: map[string]string{"Authorization": "RELAY_TEST_KEY"},
	})
	srv := proxyServer(t, h, "llm", nil, 0)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/p/v1/messages?beta=true", strings.NewReader(`{}`))
	req.Header.Set("X-API-Key", "relayer-key-must-not-leak")
	req.Header.Set("Authorization", "Bearer client-value")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Cookie", "c=1")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "sdk/1.0")
	resp, err := http.DefaultTransport.RoundTrip(req) // raw: no client-side auto-decompression
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	// Upstream side.
	if got.Get("X-Api-Key") != "" {
		t.Error("caller's relayer key forwarded upstream")
	}
	if got.Get("Authorization") != testSecret {
		t.Errorf("upstream Authorization %q, want the injected secret", got.Get("Authorization"))
	}
	if got.Get("Anthropic-Version") != "2023-06-01" || gotQuery != "beta=true" {
		t.Errorf("SDK header/query not forwarded: %q %q", got.Get("Anthropic-Version"), gotQuery)
	}
	if got.Get("Cookie") != "" || got.Get("User-Agent") == "sdk/1.0" {
		t.Errorf("cookie or client identity leaked: %q %q", got.Get("Cookie"), got.Get("User-Agent"))
	}
	// Client side.
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Request-Id") != "req_123" {
		t.Errorf("status %d / request-id %q not passed through", resp.StatusCode, resp.Header.Get("Request-Id"))
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("set-cookie/content-encoding passed through: %v", resp.Header)
	}
	if bytes.Contains(body, []byte(testSecret[:len(testSecret)/2])) || !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatalf("secret not redacted from stream: %q", body)
	}
}

func TestProxyRejectsBeforeUpstream(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer upstream.Close()
	h := proxyHandlerFor(t, HandlerConfig{}, TargetConfig{
		Name: "llm", URL: upstream.URL, Timeout: 5 * time.Second,
		AllowMethods: []string{"POST"}, AllowPaths: []string{"/v1/messages"}, MaxBodyBytes: 8,
	})
	open := proxyServer(t, h, "llm", nil, 0)
	scoped := proxyServer(t, h, "llm", []string{"other"}, 0)

	cases := []struct {
		name, base, method, path, body string
		want                           int
	}{
		{"scope", scoped.URL, "POST", "/p/v1/messages", "{}", http.StatusForbidden},
		{"method", open.URL, "GET", "/p/v1/messages", "", http.StatusForbidden},
		{"path", open.URL, "POST", "/p/v1/admin", "{}", http.StatusForbidden},
		{"encoded traversal", open.URL, "POST", "/p/v1/messages/%2e%2e/admin", "{}", http.StatusForbidden},
		{"body", open.URL, "POST", "/p/v1/messages", `{"too":"large"}`, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, c.base+c.path, strings.NewReader(c.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("upstream hit %d times by rejected requests", n)
	}
}

// The server's WriteTimeout is sized for /relay; a proxied stream must outlive it.
func TestProxyOutlivesServerWriteTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "chunk\n")
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer upstream.Close()
	h := proxyHandlerFor(t, HandlerConfig{}, TargetConfig{Name: "llm", URL: upstream.URL, Timeout: 5 * time.Second})
	srv := proxyServer(t, h, "llm", nil, 100*time.Millisecond)

	resp, err := http.Post(srv.URL+"/p/x", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != strings.Repeat("chunk\n", 3) {
		t.Fatalf("stream cut by server WriteTimeout: %q, %v", body, err)
	}
}

func TestProxyUpstreamTimeoutAndCap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			time.Sleep(time.Second)
			return
		}
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer upstream.Close()
	h := proxyHandlerFor(t, HandlerConfig{MaxResponseBytes: 10}, TargetConfig{Name: "llm", URL: upstream.URL, Timeout: 200 * time.Millisecond})
	srv := proxyServer(t, h, "llm", nil, 0)

	resp, err := http.Post(srv.URL+"/p/hang", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("hung upstream: got %d, want 502", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/p/big", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(body) != 10 {
		t.Errorf("response cap: got %d bytes, want 10", len(body))
	}
}

// Every way of splitting a stream must yield the same output as redacting it whole.
func TestStreamRedactorAnySplit(t *testing.T) {
	tg := Target{secrets: []secretHeader{{value: "sk-secret-1"}, {value: "tok-9"}}}
	in := "a sk-secret-1 b tok-9 sk-sk-secret-1 tok-tok-9 end sk-"
	want := string(tg.redact([]byte(in)))
	for i := 0; i <= len(in); i++ {
		for j := i; j <= len(in); j++ {
			r := &streamRedactor{t: tg}
			var out []byte
			for _, part := range []string{in[:i], in[i:j], in[j:]} {
				out = append(out, r.push([]byte(part))...)
			}
			out = append(out, r.flush()...)
			if string(out) != want {
				t.Fatalf("split %d/%d: got %q want %q", i, j, out, want)
			}
		}
	}
	// Truncated stream (no flush): a held-back secret prefix is never released.
	r := &streamRedactor{t: tg}
	if out := r.push([]byte("data sk-sec")); string(out) != "data " {
		t.Fatalf("held-back prefix emitted: %q", out)
	}
	// No secrets: pass-through, same slice.
	p := []byte("hello")
	if out := (&streamRedactor{}).push(p); &out[0] != &p[0] {
		t.Fatal("no-secret path copied the chunk")
	}
}
