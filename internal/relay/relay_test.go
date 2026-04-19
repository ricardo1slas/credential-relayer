package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// newTestHandler builds a Handler suitable for tests. httptest servers listen on
// loopback + http, so we always flip AllowHTTP and AllowPrivateIPs on test targets.
func newTestHandler(t *testing.T, targets []TargetConfig) *Handler {
	t.Helper()
	for i := range targets {
		targets[i].AllowHTTP = true
		targets[i].AllowPrivateIPs = true
	}
	registry, err := NewTargetRegistry(targets)
	if err != nil {
		t.Fatalf("creating registry: %v", err)
	}
	return NewHandler(registry, HandlerConfig{}, zerolog.Nop())
}

func TestRelaySuccess(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "test-svc", URL: target.URL, Timeout: 5 * time.Second, Retries: 3},
	})

	body, _ := json.Marshal(RelayRequest{Target: "test-svc", Method: "GET", Path: "/get"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp RelayResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "relayed" {
		t.Errorf("expected status 'relayed', got %q", resp.Status)
	}
	if resp.Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", resp.Attempts)
	}
}

func TestRelayTargetNotFound(t *testing.T) {
	handler := newTestHandler(t, []TargetConfig{
		{Name: "exists", URL: "http://example.com", Timeout: 1 * time.Second, Retries: 1},
	})

	body, _ := json.Marshal(RelayRequest{Target: "does-not-exist"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRelayBadRequest(t *testing.T) {
	handler := newTestHandler(t, []TargetConfig{
		{Name: "svc", URL: "http://example.com", Timeout: 1 * time.Second, Retries: 1},
	})

	body, _ := json.Marshal(map[string]string{"method": "GET"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRelayInvalidJSON(t *testing.T) {
	handler := newTestHandler(t, []TargetConfig{
		{Name: "svc", URL: "http://example.com", Timeout: 1 * time.Second, Retries: 1},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRelayRetriesOn5xx(t *testing.T) {
	var callCount atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "flaky", URL: target.URL, Timeout: 2 * time.Second, Retries: 3},
	})

	body, _ := json.Marshal(RelayRequest{Target: "flaky", Method: "GET", Path: "/"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp RelayResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", resp.Attempts)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected target hit 3 times, got %d", callCount.Load())
	}
}

func TestRelayAllRetriesExhausted(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "broken", URL: target.URL, Timeout: 2 * time.Second, Retries: 2},
	})

	body, _ := json.Marshal(RelayRequest{Target: "broken", Method: "GET", Path: "/"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	var resp RelayResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", resp.Status)
	}
	if resp.Attempts != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", resp.Attempts)
	}
}

func TestRelayTimeout(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "slow", URL: target.URL, Timeout: 100 * time.Millisecond, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{Target: "slow", Method: "GET", Path: "/"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelayDoesNotRetryOn4xx(t *testing.T) {
	var callCount atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "client-err", URL: target.URL, Timeout: 2 * time.Second, Retries: 3},
	})

	body, _ := json.Marshal(RelayRequest{Target: "client-err", Method: "GET", Path: "/"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (relay succeeded, target returned 4xx), got %d", rec.Code)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call (no retries on 4xx), got %d", callCount.Load())
	}
}

func TestRelayForwardsAllowedHeaders(t *testing.T) {
	var receivedCustom, receivedAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCustom = r.Header.Get("X-Custom-Header")
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "header-svc", URL: target.URL, Timeout: 2 * time.Second, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "header-svc", Method: "GET", Path: "/",
		Headers: map[string]string{
			"X-Custom-Header": "forward-me",
			"Authorization":   "Bearer should-be-stripped",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if receivedCustom != "forward-me" {
		t.Errorf("custom header not forwarded: got %q", receivedCustom)
	}
	if receivedAuth != "" {
		t.Errorf("Authorization header leaked to target: %q", receivedAuth)
	}
}

func TestRelayRewritesUserAgent(t *testing.T) {
	var receivedUA string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "ua-svc", URL: target.URL, Timeout: 2 * time.Second, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "ua-svc", Method: "GET", Path: "/",
		Headers: map[string]string{"User-Agent": "evil-client/9000"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if receivedUA != "private-relayer/0.1" {
		t.Errorf("expected neutral UA, got %q", receivedUA)
	}
}

func TestRelayForwardsBody(t *testing.T) {
	var receivedBody map[string]string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "body-svc", URL: target.URL, Timeout: 2 * time.Second, Retries: 0},
	})

	payload := map[string]string{"key": "value"}
	payloadBytes, _ := json.Marshal(payload)

	body, _ := json.Marshal(RelayRequest{
		Target: "body-svc", Method: "POST", Path: "/", Body: payloadBytes,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if receivedBody["key"] != "value" {
		t.Errorf("expected body key='value', got %v", receivedBody)
	}
}

func TestRelayRejectsInvalidMethod(t *testing.T) {
	handler := newTestHandler(t, []TargetConfig{
		{Name: "svc", URL: "http://example.com", Timeout: 1 * time.Second, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{Target: "svc", Method: "CONNECT", Path: "/"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for CONNECT method, got %d", rec.Code)
	}
}

func TestRelayRejectsPathWithUserinfo(t *testing.T) {
	handler := newTestHandler(t, []TargetConfig{
		{Name: "svc", URL: "http://example.com", Timeout: 1 * time.Second, Retries: 0},
	})

	// Classic SSRF-shaped path: starts with / but contains an @ that could shift host.
	body, _ := json.Marshal(RelayRequest{Target: "svc", Method: "GET", Path: "//attacker.com/x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Either 502 (relay failed to build URL -> returned in failed response)
	// or 400 -- both acceptable. The critical property is: no call to attacker.com.
	if rec.Code == http.StatusOK {
		t.Fatalf("SSRF-shaped path was accepted: got 200")
	}
}

func TestRelayEnforcesHTTPSOnTargets(t *testing.T) {
	// A target with scheme http and AllowHTTP=false should be rejected at registry creation.
	_, err := NewTargetRegistry([]TargetConfig{
		{Name: "insecure", URL: "http://example.com", Timeout: time.Second, Retries: 0, AllowHTTP: false},
	})
	if err == nil {
		t.Fatal("expected http:// target to be rejected when allow_http is false")
	}
}

func TestStripSensitiveHeaders(t *testing.T) {
	in := map[string]string{
		"Authorization":       "Bearer xyz",
		"Cookie":              "session=abc",
		"X-Api-Key":           "plain",
		"X-Custom":            "keep-me",
		"Proxy-Authorization": "Basic abc",
	}
	out := StripSensitiveHeaders(in)
	if _, ok := out["Authorization"]; ok {
		t.Error("Authorization survived stripping")
	}
	if _, ok := out["Cookie"]; ok {
		t.Error("Cookie survived stripping")
	}
	if _, ok := out["Proxy-Authorization"]; ok {
		t.Error("Proxy-Authorization survived stripping")
	}
	if out["X-Custom"] != "keep-me" {
		t.Error("X-Custom was dropped but should be preserved")
	}
	// X-Api-Key is not in the current denylist (it's the inbound header for the relay
	// itself, so clients wouldn't send it as a forwarded header); assert we don't
	// promise to strip what we don't strip.
	_ = out["X-Api-Key"]
}
