package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ricardo1slas/credential-relayer/internal/auth"
)

// unscopedCtx is a caller allowed every target (what the auth middleware sets for an
// unrestricted key). Authorize fails closed without one.
func unscopedCtx() context.Context { return auth.WithKey(context.Background(), "test", nil) }

type fakeRecorder struct {
	mu         sync.Mutex
	relays     []string // "target|key|status|failed"
	rejections []string // "target|key|reason"
	streams    []string // "target|key|bytes"
	ttfb       time.Duration
}

func (f *fakeRecorder) RecordRelay(target, key string, status, _ int, failed bool, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relays = append(f.relays, strings.Join([]string{target, key, http.StatusText(status), map[bool]string{true: "failed", false: "ok"}[failed]}, "|"))
}

func (f *fakeRecorder) RecordRejection(target, key, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejections = append(f.rejections, target+"|"+key+"|"+reason)
}

func (f *fakeRecorder) RecordStream(target, key string, ttfb, _ time.Duration, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ttfb = ttfb
	f.streams = append(f.streams, fmt.Sprintf("%s|%s|%d", target, key, bytes))
}

func TestAuthorizeRecordsRejectionReasons(t *testing.T) {
	registry, err := NewTargetRegistry([]TargetConfig{{
		Name: "api", URL: "https://api.example.com", AllowMethods: []string{"POST"}, AllowPaths: []string{"/v1"}, MaxBodyBytes: 5,
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := &fakeRecorder{}
	h := &Handler{registry: registry, metrics: rec}
	scoped := auth.WithKey(context.Background(), "phone", []string{"other"})
	open := auth.WithKey(context.Background(), "ci", nil)

	calls := []struct {
		ctx                  context.Context
		target, method, path string
		body                 int
	}{
		{scoped, "api", "POST", "/v1", 0},              // scope
		{scoped, "made-up-name-123", "POST", "/v1", 0}, // scope, unknown target
		{open, "api", "TRACE", "/v1", 0},               // bad_method
		{open, "nope", "POST", "/v1", 0},               // not_found
		{open, "api", "POST", "v1", 0},                 // bad_path
		{open, "api", "GET", "/v1", 0},                 // method
		{open, "api", "POST", "/v2", 0},                // path
		{open, "api", "POST", "/v1", 6},                // body
		{open, "api", "POST", "/v1", 5},                // allowed: not recorded
	}
	for _, c := range calls {
		_, _ = h.Authorize(c.ctx, c.target, c.method, c.path, c.body)
	}
	want := []string{
		"api|phone|scope",
		"unknown|phone|scope", // client-chosen names never become label values
		"api|ci|bad_method",
		"unknown|ci|not_found",
		"api|ci|bad_path",
		"api|ci|method",
		"api|ci|path",
		"api|ci|body",
	}
	if strings.Join(rec.rejections, ",") != strings.Join(want, ",") {
		t.Fatalf("rejections\n got %v\nwant %v", rec.rejections, want)
	}
}

func TestProxyRecordsKeyAndStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "hello")
	}))
	defer upstream.Close()
	h := proxyHandlerFor(t, HandlerConfig{}, TargetConfig{Name: "llm", URL: upstream.URL, Timeout: 5 * time.Second})
	rec := &fakeRecorder{}
	h.SetMetrics(rec)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Proxy(w, r.WithContext(auth.WithKey(r.Context(), "gemini-demo", nil)), "llm", "/x")
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if len(rec.relays) != 1 || !strings.HasPrefix(rec.relays[0], "llm|gemini-demo|OK|ok") {
		t.Fatalf("relay records %v", rec.relays)
	}
	if len(rec.streams) != 1 || rec.streams[0] != "llm|gemini-demo|5" {
		t.Fatalf("stream records %v", rec.streams)
	}
	if rec.ttfb < 50*time.Millisecond || rec.ttfb > 2*time.Second {
		t.Fatalf("ttfb %v, want >= upstream delay", rec.ttfb)
	}
}

func TestWorstCase(t *testing.T) {
	cases := []struct {
		retries int
		timeout time.Duration
		min     time.Duration
		max     time.Duration
	}{
		{0, 5 * time.Second, 5 * time.Second, 5 * time.Second},
		{3, 5 * time.Second, 20*time.Second + 3500*time.Millisecond, 20*time.Second + 3500*time.Millisecond}, // 4x5s + .5+1+2s
		{40, time.Second, 41 * time.Second, 41*time.Second + 40*maxDelay},                                    // capped, no overflow
	}
	for _, c := range cases {
		got := Target{Retries: c.retries, Timeout: c.timeout}.worstCase()
		if got < c.min || got > c.max {
			t.Errorf("retries=%d timeout=%v: worstCase %v not in [%v, %v]", c.retries, c.timeout, got, c.min, c.max)
		}
	}
}

// /relay lifts the server's WriteTimeout to the target's worst case, so a slow upstream (an LLM
// taking longer than write_timeout) still gets its answer back.
func TestRelayOutlivesServerWriteTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	h := newTestHandler(t, []TargetConfig{{Name: "slow", URL: upstream.URL, Timeout: 2 * time.Second, Retries: 1}})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithKey(r.Context(), "k", nil)))
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"target":"slow","method":"GET","path":"/"}`))
	if err != nil {
		t.Fatalf("response cut by server WriteTimeout: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}
