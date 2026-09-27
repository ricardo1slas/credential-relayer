package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

const testSecret = "fake-injected-secret-value"

func TestResolveSecrets(t *testing.T) {
	t.Setenv("RELAY_TEST_KEY", testSecret)
	t.Setenv("RELAY_TEST_EMPTY", "")
	t.Setenv("RELAY_TEST_NEWLINE", "abc\r\nX-Evil: 1")

	tests := []struct {
		name    string
		in      map[string]string
		wantErr string
	}{
		{"ok", map[string]string{"x-api-key": "RELAY_TEST_KEY"}, ""},
		{"authorization allowed", map[string]string{"Authorization": "RELAY_TEST_KEY"}, ""},
		{"unset env", map[string]string{"X-Api-Key": "RELAY_TEST_UNSET_XYZ"}, "unset or empty"},
		{"empty env", map[string]string{"X-Api-Key": "RELAY_TEST_EMPTY"}, "unset or empty"},
		{"newline value", map[string]string{"X-Api-Key": "RELAY_TEST_NEWLINE"}, "newline"},
		{"framing header", map[string]string{"Content_Length": "RELAY_TEST_KEY"}, "cannot be a secret"},
		{"host header", map[string]string{"Host": "RELAY_TEST_KEY"}, "cannot be a secret"},
		{"invalid name", map[string]string{"X Api:Key": "RELAY_TEST_KEY"}, "invalid secret header name"},
		{"empty name", map[string]string{"": "RELAY_TEST_KEY"}, "invalid secret header name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSecrets("svc", tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(got) != 1 || got[0].value != testSecret {
					t.Fatalf("got %+v", got)
				}
				if got[0].name != http.CanonicalHeaderKey(got[0].name) {
					t.Fatalf("name not canonical: %q", got[0].name)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
			if strings.Contains(err.Error(), testSecret) {
				t.Fatal("error message leaks the secret value")
			}
		})
	}
}

func TestNewTargetRegistryFailsOnMissingSecret(t *testing.T) {
	_, err := NewTargetRegistry([]TargetConfig{{
		Name: "svc", URL: "https://example.com",
		SecretHeaders: map[string]string{"X-Api-Key": "RELAY_TEST_UNSET_XYZ"},
	}})
	if err == nil {
		t.Fatal("expected startup error for unset secret env var")
	}
}

func TestIsSecretHeader(t *testing.T) {
	tg := Target{secrets: []secretHeader{{name: "X-Api-Key", folded: "x-api-key", value: testSecret}}}
	for _, h := range []string{"X-Api-Key", "x-api-key", "X_API_KEY", " x_Api-key "} {
		if !tg.isSecretHeader(h) {
			t.Errorf("%q should collide with the secret header", h)
		}
	}
	if tg.isSecretHeader("X-Api-Keys") || tg.isSecretHeader("Accept") {
		t.Error("unrelated header treated as secret")
	}
	if (Target{}).isSecretHeader("X-Api-Key") {
		t.Error("target without secrets reported a secret header")
	}
}

func TestRedact(t *testing.T) {
	tg := Target{secrets: []secretHeader{{name: "X-Api-Key", value: testSecret}, {name: "Authorization", value: "Bearer tok-xyz"}}}
	in := []byte(`{"headers":{"X-Api-Key":"` + testSecret + `","Authorization":"Bearer tok-xyz"},"again":"` + testSecret + `"}`)
	out := tg.redact(in)
	if bytes.Contains(out, []byte(testSecret)) || bytes.Contains(out, []byte("tok-xyz")) {
		t.Fatalf("secret survived redaction: %s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("redaction broke JSON: %s", out)
	}
	clean := []byte(`{"ok":true}`)
	if got := tg.redact(clean); !bytes.Equal(got, clean) {
		t.Fatalf("clean body changed: %s", got)
	}
}

// echoHeaders is an upstream that returns every value it received for X-Api-Key.
func echoHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"x-api-key": r.Header.Values("X-Api-Key")})
}

func TestRelayInjectsSecretAndRedactsEcho(t *testing.T) {
	t.Setenv("RELAY_TEST_KEY", testSecret)
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Values("X-Api-Key")
		echoHeaders(w, r)
	}))
	defer upstream.Close()

	h := newTestHandler(t, []TargetConfig{{
		Name: "svc", URL: upstream.URL, Timeout: 5 * time.Second,
		SecretHeaders: map[string]string{"X-Api-Key": "RELAY_TEST_KEY"},
	}})

	// The client tries to override the secret in both spellings.
	body, _ := json.Marshal(RelayRequest{
		Target: "svc", Method: "GET", Path: "/headers",
		Headers: map[string]string{"x-api-key": "client-value", "X_Api_Key": "client-value"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(seen) != 1 || seen[0] != testSecret {
		t.Fatalf("upstream saw X-Api-Key %q, want exactly the injected secret", seen)
	}
	if strings.Contains(rec.Body.String(), testSecret) {
		t.Fatalf("secret echoed back to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[REDACTED]") {
		t.Fatalf("expected redaction marker: %s", rec.Body.String())
	}
}

func TestExecuteRelayInjectsSecret(t *testing.T) {
	t.Setenv("RELAY_TEST_KEY", testSecret)
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer upstream.Close()

	h := newTestHandler(t, []TargetConfig{{
		Name: "svc", URL: upstream.URL, Timeout: 5 * time.Second,
		SecretHeaders: map[string]string{"Authorization": "RELAY_TEST_KEY"},
	}})
	resp, _, err := h.ExecuteRelay(unscopedCtx(), "svc", "POST", "/hook", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if seen != testSecret {
		t.Fatalf("worker path: upstream saw Authorization %q", seen)
	}
}

func TestSecretOnRedirect(t *testing.T) {
	t.Setenv("RELAY_TEST_KEY", testSecret)

	var otherSaw, sameSaw string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherSaw = r.Header.Get("X-Api-Key")
	}))
	defer other.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
		case "/same":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/landed":
			sameSaw = r.Header.Get("X-Api-Key")
		}
	}))
	defer origin.Close()

	registry, err := NewTargetRegistry([]TargetConfig{{
		Name: "svc", URL: origin.URL, Timeout: 5 * time.Second, AllowHTTP: true, AllowPrivateIPs: true,
		SecretHeaders: map[string]string{"X-Api-Key": "RELAY_TEST_KEY"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(registry, HandlerConfig{MaxRedirects: 3}, zerolog.Nop())

	for _, path := range []string{"/cross", "/same"} {
		resp, _, err := h.ExecuteRelay(unscopedCtx(), "svc", "GET", path, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if otherSaw != "" {
		t.Fatalf("secret leaked to cross-host redirect target: %q", otherSaw)
	}
	if sameSaw != testSecret {
		t.Fatalf("same-origin redirect lost the secret: %q", sameSaw)
	}
}
