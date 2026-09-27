package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeMethod(t *testing.T) {
	for in, want := range map[string]string{"": "POST", "get": "GET", "Patch": "PATCH", "DELETE": "DELETE"} {
		if got := NormalizeMethod(in); got != want {
			t.Errorf("NormalizeMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizePolicy(t *testing.T) {
	tests := []struct {
		name    string
		tc      TargetConfig
		wantErr string
	}{
		{"empty", TargetConfig{Name: "t"}, ""},
		{"valid", TargetConfig{Name: "t", AllowMethods: []string{"post", " get "}, AllowPaths: []string{"/v1/messages", "/"}, MaxBodyBytes: 10}, ""},
		{"bad method", TargetConfig{Name: "t", AllowMethods: []string{"CONNECT"}}, "allow_methods"},
		{"relative path", TargetConfig{Name: "t", AllowPaths: []string{"v1"}}, "allow_paths"},
		{"dotdot path", TargetConfig{Name: "t", AllowPaths: []string{"/v1/../admin"}}, "allow_paths"},
		{"encoded path", TargetConfig{Name: "t", AllowPaths: []string{"/v1%2f"}}, "allow_paths"},
		{"query in path", TargetConfig{Name: "t", AllowPaths: []string{"/v1?x=1"}}, "allow_paths"},
		{"negative body", TargetConfig{Name: "t", MaxBodyBytes: -1}, "max_body_bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methods, _, err := normalizePolicy(tt.tc)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range methods {
					if m != strings.ToUpper(strings.TrimSpace(m)) {
						t.Errorf("method %q not normalized", m)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestPathAllowed(t *testing.T) {
	tg := Target{allowPaths: []string{"/v1/messages", "/health/"}}
	cases := map[string]bool{
		"/v1/messages":           true,
		"/v1/messages/batches":   true,
		"/health":                false, // prefix "/health/" only matches below it
		"/health/live":           true,
		"/v1/messagesX":          false, // sibling, not a sub-path
		"/v1/models":             false,
		"":                       false, // treated as "/"
		"/v1/messages/%2e%2e/x":  false,
		"/v1/messages/%2E%2E/x":  false,
		"/v1/messages%2fadmin":   false,
		"/v1/messages/%5cadmin":  false,
		`/v1/messages/\admin`:    false,
		"/v1/messages/..;/admin": false,
	}
	for p, want := range cases {
		if got := tg.pathAllowed(p); got != want {
			t.Errorf("pathAllowed(%q) = %v, want %v", p, got, want)
		}
	}
	if !(Target{allowPaths: []string{"/"}}).pathAllowed("") {
		t.Error(`policy "/" should allow the base path`)
	}
}

func TestCheckPolicy(t *testing.T) {
	tg := Target{allowMethods: []string{"POST"}, allowPaths: []string{"/v1"}, maxBodyBytes: 10}
	cases := []struct {
		method, path string
		body         int
		want         int // 0 = allowed
	}{
		{"POST", "/v1/x", 10, 0},
		{"GET", "/v1/x", 0, http.StatusForbidden},
		{"POST", "/v2", 0, http.StatusForbidden},
		{"POST", "/v1", 11, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		err := tg.checkPolicy(c.method, c.path, c.body)
		var rerr *RequestError
		switch {
		case c.want == 0 && err != nil:
			t.Errorf("%s %s: unexpected %v", c.method, c.path, err)
		case c.want != 0 && (!errors.As(err, &rerr) || rerr.Status != c.want):
			t.Errorf("%s %s body=%d: want %d, got %v", c.method, c.path, c.body, c.want, err)
		}
	}
	if err := (Target{}).checkPolicy("DELETE", "/anything", 1<<20); err != nil {
		t.Errorf("target without policy should allow everything: %v", err)
	}
}

func TestAuthorize(t *testing.T) {
	registry, err := NewTargetRegistry([]TargetConfig{{
		Name: "api", URL: "https://api.example.com",
		AllowMethods: []string{"POST"}, AllowPaths: []string{"/v1/messages"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{registry: registry, metrics: nopRecorder{}}
	cases := []struct {
		target, method, path string
		want                 int
	}{
		{"api", "POST", "/v1/messages", 0},
		{"api", "TRACE", "/v1/messages", http.StatusBadRequest},
		{"nope", "POST", "/v1/messages", http.StatusNotFound},
		{"api", "POST", "v1/messages", http.StatusBadRequest},            // not absolute
		{"api", "POST", "//evil.com/v1/messages", http.StatusBadRequest}, // host change
		{"api", "POST", "/v1/messages/../admin", http.StatusForbidden},   // judged after resolution
		{"api", "GET", "/v1/messages", http.StatusForbidden},
	}
	for _, c := range cases {
		_, err := h.Authorize(unscopedCtx(), c.target, c.method, c.path, 0)
		var rerr *RequestError
		if c.want == 0 {
			if err != nil {
				t.Errorf("%s %s %s: unexpected %v", c.target, c.method, c.path, err)
			}
			continue
		}
		if !errors.As(err, &rerr) || rerr.Status != c.want {
			t.Errorf("%s %s %s: want %d, got %v", c.target, c.method, c.path, c.want, err)
		}
	}
}

func TestRelayPolicyBlocksBeforeUpstream(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer upstream.Close()
	h := newTestHandler(t, []TargetConfig{{
		Name: "svc", URL: upstream.URL, Timeout: 5 * time.Second,
		AllowMethods: []string{"POST"}, AllowPaths: []string{"/ok"},
	}})

	send := func(method, path string) int {
		body, _ := json.Marshal(RelayRequest{Target: "svc", Method: method, Path: path})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(bytes.NewReader(body)))
		return rec.Code
	}
	if code := send("POST", "/nope"); code != http.StatusForbidden {
		t.Fatalf("disallowed path: got %d", code)
	}
	if code := send("GET", "/ok"); code != http.StatusForbidden {
		t.Fatalf("disallowed method: got %d", code)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("upstream hit %d times by rejected requests", n)
	}
	if code := send("POST", "/ok"); code != http.StatusOK {
		t.Fatalf("allowed request: got %d", code)
	}
}

// The worker path re-checks policy, so a job queued before a config change can't bypass it.
func TestExecuteRelayEnforcesPolicy(t *testing.T) {
	h := newTestHandler(t, []TargetConfig{{Name: "svc", URL: "http://127.0.0.1:1", AllowPaths: []string{"/ok"}}})
	_, _, err := h.ExecuteRelay(unscopedCtx(), "svc", "POST", "/nope", nil, nil)
	var rerr *RequestError
	if !errors.As(err, &rerr) || rerr.Status != http.StatusForbidden {
		t.Fatalf("want 403 RequestError, got %v", err)
	}
}
