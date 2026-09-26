package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]KeyConfig{
		{Name: "test-client", Key: "secret-123"},
	})

	var capturedName string
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedName = KeyName(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "secret-123")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedName != "test-client" {
		t.Errorf("expected key name 'test-client', got %q", capturedName)
	}
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	auth := NewAPIKeyAuth([]KeyConfig{
		{Name: "test-client", Key: "secret-123"},
	})

	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "missing API key" {
		t.Errorf("expected 'missing API key', got %q", body["error"])
	}
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]KeyConfig{
		{Name: "test-client", Key: "secret-123"},
	})

	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "invalid API key" {
		t.Errorf("expected 'invalid API key', got %q", body["error"])
	}
}

func TestAPIKeyAuth_MultipleKeys(t *testing.T) {
	auth := NewAPIKeyAuth([]KeyConfig{
		{Name: "client-a", Key: "key-aaa"},
		{Name: "client-b", Key: "key-bbb"},
	})

	var capturedName string
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedName = KeyName(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// Test second key works.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "key-bbb")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedName != "client-b" {
		t.Errorf("expected 'client-b', got %q", capturedName)
	}
}

func TestTargetAllowed(t *testing.T) {
	cases := []struct {
		name    string
		ctx     func() context.Context
		target  string
		allowed bool
	}{
		{"listed", func() context.Context { return WithKey(context.Background(), "k", []string{"a", "b"}) }, "a", true},
		{"not listed", func() context.Context { return WithKey(context.Background(), "k", []string{"a"}) }, "b", false},
		{"wildcard", func() context.Context { return WithKey(context.Background(), "k", []string{"*"}) }, "anything", true},
		{"empty list = all (backward compatible)", func() context.Context { return WithKey(context.Background(), "k", nil) }, "anything", true},
		{"no principal fails closed", context.Background, "a", false},
	}
	for _, c := range cases {
		if got := TargetAllowed(c.ctx(), c.target); got != c.allowed {
			t.Errorf("%s: TargetAllowed(%q) = %v, want %v", c.name, c.target, got, c.allowed)
		}
	}
}
