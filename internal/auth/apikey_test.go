package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// Google SDKs send the key as x-goog-api-key; X-API-Key wins when both are present.
func TestAPIKeyAuth_GoogHeader(t *testing.T) {
	a := NewAPIKeyAuth([]KeyConfig{{Name: "goog", Key: "g-key"}, {Name: "std", Key: "s-key"}})
	var name string
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { name = KeyName(r.Context()) }))

	cases := []struct {
		headers  map[string]string
		wantCode int
		wantName string
	}{
		{map[string]string{"x-goog-api-key": "g-key"}, http.StatusOK, "goog"},
		{map[string]string{"X-API-Key": "s-key", "X-Goog-Api-Key": "g-key"}, http.StatusOK, "std"},
		{map[string]string{"X-Goog-Api-Key": "wrong"}, http.StatusUnauthorized, ""},
	}
	for _, c := range cases {
		name = ""
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.wantCode || name != c.wantName {
			t.Errorf("%v: got %d/%q, want %d/%q", c.headers, rec.Code, name, c.wantCode, c.wantName)
		}
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

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func call(a *APIKeyAuth, key string) (int, string) {
	var name string
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { name = KeyName(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, name + "|" + rec.Body.String()
}

func TestHashedKeyAuthenticates(t *testing.T) {
	a := NewAPIKeyAuth([]KeyConfig{{Name: "svc", KeySHA256: sha("s3cret-value")}})
	if code, body := call(a, "s3cret-value"); code != 200 || body[:3] != "svc" {
		t.Fatalf("correct key: got %d %q", code, body)
	}
	if code, _ := call(a, sha("s3cret-value")); code != 401 { // presenting the hash itself must not work
		t.Fatalf("the stored hash must not authenticate, got %d", code)
	}
	if code, _ := call(a, "wrong"); code != 401 {
		t.Fatalf("wrong key: got %d", code)
	}
}

func TestExpiredKeyRejectedIndistinguishably(t *testing.T) {
	a := NewAPIKeyAuth([]KeyConfig{{Name: "old", Key: "k1", ExpiresAt: "2026-01-01"}, {Name: "new", Key: "k2", ExpiresAt: "2099-01-01"}})
	a.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }

	codeExpired, bodyExpired := call(a, "k1")
	codeUnknown, bodyUnknown := call(a, "nope")
	if codeExpired != 401 || codeUnknown != 401 {
		t.Fatalf("expired=%d unknown=%d, both must be 401", codeExpired, codeUnknown)
	}
	if bodyExpired[len("|"):] != bodyUnknown[len("|"):] {
		t.Fatalf("expired and unknown keys must look identical: %q vs %q", bodyExpired, bodyUnknown)
	}
	if code, _ := call(a, "k2"); code != 200 {
		t.Fatalf("unexpired key: got %d", code)
	}
	a.now = func() time.Time { return time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC) } // date-only expiry = end of that day
	if code, _ := call(a, "k1"); code != 200 {
		t.Fatalf("key should still be valid on its expiry day, got %d", code)
	}
}

func TestRotationOverlapSharesIdentity(t *testing.T) {
	a := NewAPIKeyAuth([]KeyConfig{{Name: "svc", Key: "old-key"}, {Name: "svc", Key: "new-key"}})
	for _, k := range []string{"old-key", "new-key"} {
		if code, body := call(a, k); code != 200 || body[:3] != "svc" {
			t.Fatalf("%s: got %d %q", k, code, body)
		}
	}
}

func TestKeyConfigValidate(t *testing.T) {
	good := sha("x")
	long := strings.Repeat("k", MinKeyLength)
	cases := []struct {
		name string
		k    KeyConfig
		ok   bool
	}{
		{"plaintext", KeyConfig{Name: "a", Key: long}, true},
		{"plaintext too short", KeyConfig{Name: "a", Key: long[1:]}, false},
		{"hashed", KeyConfig{Name: "a", KeySHA256: good}, true},
		{"no name", KeyConfig{Key: long}, false},
		{"neither key nor hash", KeyConfig{Name: "a"}, false},
		{"both key and hash", KeyConfig{Name: "a", Key: long, KeySHA256: good}, false},
		{"short hash", KeyConfig{Name: "a", KeySHA256: "abcd"}, false},
		{"non-hex hash", KeyConfig{Name: "a", KeySHA256: good[:63] + "z"}, false},
		{"bad expiry", KeyConfig{Name: "a", Key: long, ExpiresAt: "next tuesday"}, false},
		{"good expiry", KeyConfig{Name: "a", Key: long, ExpiresAt: "2027-03-01"}, true},
	}
	for _, c := range cases {
		if err := c.k.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
