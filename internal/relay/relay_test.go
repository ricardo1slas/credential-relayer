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

func newTestHandler(t *testing.T, targets []TargetConfig) *Handler {
	t.Helper()
	registry, err := NewTargetRegistry(targets)
	if err != nil {
		t.Fatalf("creating registry: %v", err)
	}
	logger := zerolog.Nop()
	return NewHandler(registry, logger)
}

func TestRelaySuccess(t *testing.T) {
	// Start a mock target server that returns 200.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "test-svc", URL: target.URL, Timeout: 5 * time.Second, Retries: 3},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "test-svc",
		Method: "GET",
		Path:   "/get",
	})

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
		{Name: "exists", URL: "http://localhost", Timeout: 1 * time.Second, Retries: 1},
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
		{Name: "svc", URL: "http://localhost", Timeout: 1 * time.Second, Retries: 1},
	})

	// Missing target field.
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
		{Name: "svc", URL: "http://localhost", Timeout: 1 * time.Second, Retries: 1},
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

	// Server fails twice with 500, then succeeds.
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

	body, _ := json.Marshal(RelayRequest{
		Target: "flaky",
		Method: "GET",
		Path:   "/",
	})

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
	// Server always returns 500.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "broken", URL: target.URL, Timeout: 2 * time.Second, Retries: 2},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "broken",
		Method: "GET",
		Path:   "/",
	})

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
	// Server takes too long to respond.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "slow", URL: target.URL, Timeout: 100 * time.Millisecond, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "slow",
		Method: "GET",
		Path:   "/",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelayDoesNotRetryOn4xx(t *testing.T) {
	var callCount atomic.Int32

	// Server always returns 400.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "client-err", URL: target.URL, Timeout: 2 * time.Second, Retries: 3},
	})

	body, _ := json.Marshal(RelayRequest{
		Target: "client-err",
		Method: "GET",
		Path:   "/",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// 4xx from the target is relayed as-is, no retries.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (relay succeeded, target returned 4xx), got %d", rec.Code)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call (no retries on 4xx), got %d", callCount.Load())
	}
}

func TestRelayForwardsHeaders(t *testing.T) {
	var receivedHeader string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom-Header")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler := newTestHandler(t, []TargetConfig{
		{Name: "header-svc", URL: target.URL, Timeout: 2 * time.Second, Retries: 0},
	})

	body, _ := json.Marshal(RelayRequest{
		Target:  "header-svc",
		Method:  "GET",
		Path:    "/",
		Headers: map[string]string{"X-Custom-Header": "hello-from-relayer"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if receivedHeader != "hello-from-relayer" {
		t.Errorf("expected header 'hello-from-relayer', got %q", receivedHeader)
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
		Target: "body-svc",
		Method: "POST",
		Path:   "/",
		Body:   payloadBytes,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/relay", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if receivedBody["key"] != "value" {
		t.Errorf("expected body key='value', got %v", receivedBody)
	}
}
