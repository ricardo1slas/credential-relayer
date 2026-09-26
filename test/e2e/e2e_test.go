// Package e2e exercises the relayer as a black box: it wires the real router
// (auth + rate limit + relay + queue + metrics) in front of a mock upstream and
// drives it through the public SDK and raw HTTP, just like a deployed instance.
//
// Tests that need Redis skip cleanly when it is unavailable (same convention as
// internal/queue tests), so the suite stays green in environments without it.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/ricardo1slas/private_relayer/internal/api"
	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/ricardo1slas/private_relayer/internal/metrics"
	"github.com/ricardo1slas/private_relayer/internal/queue"
	"github.com/ricardo1slas/private_relayer/internal/ratelimit"
	"github.com/ricardo1slas/private_relayer/internal/relay"
	"github.com/ricardo1slas/private_relayer/pkg/client"
)

const (
	testAPIKey   = "e2e-secret-key" // #nosec G101 -- test fixture, not a real credential
	scopedAPIKey = "e2e-scoped-key" // #nosec G101 -- test fixture; only allowed target "other"
	otherAPIKey  = "e2e-other-key"  // #nosec G101 -- test fixture; a second unrestricted tenant
)

type stack struct {
	server  *httptest.Server
	metrics *metrics.Metrics
	queue   *queue.Queue
	handler *relay.Handler
	redis   *redis.Client
}

type stackOpts struct {
	upstreamURL string
	rateLimit   int           // requests per window; 0 -> 100
	window      time.Duration // 0 -> time.Minute
}

// newStack builds the full router in front of a (possibly lazy) Redis client.
func newStack(t *testing.T, opts stackOpts) *stack {
	t.Helper()

	reqs := opts.rateLimit
	if reqs == 0 {
		reqs = 100
	}
	win := opts.window
	if win == 0 {
		win = time.Minute
	}

	registry, err := relay.NewTargetRegistry([]relay.TargetConfig{{
		Name:            "mock",
		URL:             opts.upstreamURL,
		Timeout:         3 * time.Second,
		Retries:         2,
		AllowHTTP:       true, // httptest serves plain http on loopback
		AllowPrivateIPs: true,
	}, {
		// Same upstream behind a request policy.
		Name: "locked", URL: opts.upstreamURL, Timeout: 3 * time.Second, Retries: 2,
		AllowHTTP: true, AllowPrivateIPs: true,
		AllowMethods: []string{"POST"}, AllowPaths: []string{"/v1/messages"}, MaxBodyBytes: 64,
	}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	m := metrics.New()
	handler := relay.NewHandler(registry, relay.HandlerConfig{}, zerolog.Nop())
	handler.SetMetrics(m)

	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rc := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	q := queue.NewQueue(rc, queue.Config{}, zerolog.Nop())

	router := api.NewRouter(api.RouterDeps{
		Logger:       zerolog.Nop(),
		RelayHandler: handler,
		APIKeyAuth: auth.NewAPIKeyAuth([]auth.KeyConfig{
			{Name: "e2e", Key: testAPIKey},
			{Name: "scoped", Key: scopedAPIKey, Targets: []string{"other"}},
			{Name: "tenant-b", Key: otherAPIKey},
		}),
		Limiter:         ratelimit.New(ratelimit.Config{Requests: reqs, Window: win}),
		Queue:           q,
		RedisClient:     rc,
		Metrics:         m,
		MaxRequestBytes: 1 << 20,
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = rc.Close() })

	return &stack{server: srv, metrics: m, queue: q, handler: handler, redis: rc}
}

func (s *stack) requireRedis(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.redis.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}
	s.redis.FlushDB(ctx)
}

func TestE2ERelayThroughSDK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Custom"); got != "v1" {
			t.Errorf("upstream missing forwarded header X-Custom: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
	}))
	defer upstream.Close()

	s := newStack(t, stackOpts{upstreamURL: upstream.URL})
	c := client.New(s.server.URL, testAPIKey)

	resp, err := c.Relay(context.Background(), client.RelayRequest{
		Target:  "mock",
		Method:  "GET",
		Path:    "/data",
		Headers: map[string]string{"X-Custom": "v1"},
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if resp.Status != "relayed" || resp.ResponseCode != 200 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(string(resp.ResponseBody), "world") {
		t.Errorf("response body missing upstream payload: %s", resp.ResponseBody)
	}
}

func TestE2EUnauthorized(t *testing.T) {
	s := newStack(t, stackOpts{upstreamURL: "http://127.0.0.1:1"})

	req, _ := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/relay", strings.NewReader(`{"target":"mock"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestE2EForwards4xxWithoutRetry(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such resource"})
	}))
	defer upstream.Close()

	s := newStack(t, stackOpts{upstreamURL: upstream.URL})
	c := client.New(s.server.URL, testAPIKey)

	resp, err := c.Relay(context.Background(), client.RelayRequest{Target: "mock", Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if resp.ResponseCode != http.StatusNotFound {
		t.Errorf("response_code = %d, want 404", resp.ResponseCode)
	}
	if calls != 1 {
		t.Errorf("upstream called %d times, want 1 (4xx must not retry)", calls)
	}
}

func TestE2ERateLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := newStack(t, stackOpts{upstreamURL: upstream.URL, rateLimit: 2, window: time.Minute})

	do := func() int {
		req, _ := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/relay", strings.NewReader(`{"target":"mock","method":"GET","path":"/"}`))
		req.Header.Set("X-API-Key", testAPIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if c1, c2 := do(), do(); c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("first two requests = %d, %d; want 200, 200", c1, c2)
	}
	if c3 := do(); c3 != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", c3)
	}
}

func TestE2EMetricsRecorded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := newStack(t, stackOpts{upstreamURL: upstream.URL})
	c := client.New(s.server.URL, testAPIKey)
	if _, err := c.Relay(context.Background(), client.RelayRequest{Target: "mock", Method: "GET", Path: "/"}); err != nil {
		t.Fatalf("Relay: %v", err)
	}

	rec := httptest.NewRecorder()
	s.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `relayer_relay_requests_total{outcome="success",target="mock"}`) {
		t.Errorf("metrics missing relay success counter for target mock:\n%s", body)
	}
	if !strings.Contains(body, "relayer_http_requests_total") {
		t.Errorf("metrics missing http request counter")
	}
}

func TestE2EWebhookJobFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := newStack(t, stackOpts{upstreamURL: upstream.URL})
	s.requireRedis(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wp := queue.NewWorkerPool(s.queue, s.handler, queue.WorkerPoolConfig{Workers: 2, RetryDelay: time.Second}, zerolog.Nop())
	wp.SetMetrics(s.metrics)
	go wp.Start(ctx)

	c := client.New(s.server.URL, testAPIKey)
	jobID, err := c.Webhook(context.Background(), client.WebhookRequest{Target: "mock", Method: "POST", Path: "/hook"})
	if err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	if jobID == "" {
		t.Fatal("expected non-empty job ID")
	}

	// Poll until the worker marks it completed.
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		job, err := c.JobStatus(context.Background(), jobID)
		if err == nil {
			last = job.Status
			if job.Status == "completed" {
				if job.ResponseCode != http.StatusOK {
					t.Errorf("job response_code = %d, want 200", job.ResponseCode)
				}
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("job did not complete in time; last status = %q", last)
}

func TestE2EJobNotFound(t *testing.T) {
	s := newStack(t, stackOpts{upstreamURL: "http://127.0.0.1:1"})
	s.requireRedis(t)

	c := client.New(s.server.URL, testAPIKey)
	_, err := c.JobStatus(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing job")
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 APIError, got %v", err)
	}
}

func TestE2EKeyScopeEnforced(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer upstream.Close()
	s := newStack(t, stackOpts{upstreamURL: upstream.URL})

	scoped := client.New(s.server.URL, scopedAPIKey)
	var apiErr *client.APIError

	_, err := scoped.Relay(context.Background(), client.RelayRequest{Target: "mock", Method: "GET", Path: "/"})
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("relay to out-of-scope target: expected 403, got %v", err)
	}
	_, err = scoped.Webhook(context.Background(), client.WebhookRequest{Target: "mock", Method: "POST", Path: "/"})
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("webhook to out-of-scope target: expected 403, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("upstream reached %d times by a key that is not allowed to use it", hits)
	}

	// The unrestricted key still works.
	if _, err := client.New(s.server.URL, testAPIKey).Relay(context.Background(), client.RelayRequest{Target: "mock", Method: "GET", Path: "/"}); err != nil {
		t.Fatalf("unrestricted key: %v", err)
	}
}

// Policy is enforced on both entry points before anything reaches the upstream; for /webhook
// that means at enqueue (no Redis needed: rejected requests never touch the queue).
func TestE2ETargetPolicy(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer upstream.Close()
	s := newStack(t, stackOpts{upstreamURL: upstream.URL})
	c := client.New(s.server.URL, testAPIKey)
	big := json.RawMessage(`"` + strings.Repeat("x", 100) + `"`)

	cases := []struct {
		name   string
		method string
		path   string
		body   json.RawMessage
		want   int
	}{
		{"method not allowed", "GET", "/v1/messages", nil, http.StatusForbidden},
		{"path not allowed", "POST", "/v1/admin", nil, http.StatusForbidden},
		{"traversal out of prefix", "POST", "/v1/messages/../admin", nil, http.StatusForbidden},
		{"encoded traversal", "POST", "/v1/messages/%2e%2e/admin", nil, http.StatusForbidden},
		{"body too large", "POST", "/v1/messages", big, http.StatusRequestEntityTooLarge},
	}
	var apiErr *client.APIError
	for _, tc := range cases {
		_, err := c.Relay(context.Background(), client.RelayRequest{Target: "locked", Method: tc.method, Path: tc.path, Body: tc.body})
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.want {
			t.Errorf("relay %s: want %d, got %v", tc.name, tc.want, err)
		}
		_, err = c.Webhook(context.Background(), client.WebhookRequest{Target: "locked", Method: tc.method, Path: tc.path, Body: tc.body})
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.want {
			t.Errorf("webhook %s: want %d, got %v", tc.name, tc.want, err)
		}
	}
	// Unknown targets are now refused at enqueue instead of dead-lettering later.
	_, err := c.Webhook(context.Background(), client.WebhookRequest{Target: "nope", Method: "POST", Path: "/"})
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("webhook unknown target: want 404, got %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("upstream reached %d times by requests the policy should reject", n)
	}

	if _, err := c.Relay(context.Background(), client.RelayRequest{Target: "locked", Method: "POST", Path: "/v1/messages", Body: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatalf("allowed request: %v", err)
	}
}

// Through the real router: SDK-style auth (x-api-key), path + query extraction, and streaming
// that survives the logging/metrics response-writer wrappers.
func TestE2EProxyStreams(t *testing.T) {
	release := make(chan struct{})
	var gotPath, gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.RequestURI(), r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	defer upstream.Close()
	defer close(release)
	s := newStack(t, stackOpts{upstreamURL: upstream.URL})

	req, _ := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/proxy/mock/v1/messages?beta=true", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", testAPIKey) // what the Anthropic SDK sends with ANTHROPIC_API_KEY=<relayer key>
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	first := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(resp.Body).ReadString('\n'); first <- line }()
	select {
	case got := <-first:
		if got != "data: one\n" {
			t.Fatalf("first line %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event not streamed through the router")
	}
	if gotPath != "/v1/messages?beta=true" || gotKey != "" {
		t.Fatalf("upstream saw path %q, x-api-key %q (relayer key must not be forwarded)", gotPath, gotKey)
	}

	// Scoped keys are enforced here too.
	req, _ = http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/proxy/mock/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-API-Key", scopedAPIKey)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped key: got %d, want 403", resp2.StatusCode)
	}
}

func TestE2EJobOwnership(t *testing.T) {
	s := newStack(t, stackOpts{upstreamURL: "http://127.0.0.1:1"})
	s.requireRedis(t)

	owner := client.New(s.server.URL, testAPIKey)
	jobID, err := owner.Webhook(context.Background(), client.WebhookRequest{Target: "mock", Method: "POST", Path: "/x"})
	if err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	if _, err := owner.JobStatus(context.Background(), jobID); err != nil {
		t.Fatalf("owner cannot read own job: %v", err)
	}

	_, err = client.New(s.server.URL, otherAPIKey).JobStatus(context.Background(), jobID)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("another key reading the job: expected 404, got %v", err)
	}
}
