package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// #nosec G101 -- this is the header *name*, not a credential value.
const headerAPIKey = "X-API-Key"

type KeyConfig struct {
	Name    string   `koanf:"name"`
	Key     string   `koanf:"key"`
	Targets []string `koanf:"targets"` // allowed relay targets; empty or ["*"] = all
}

type APIKeyAuth struct {
	keys map[string]*Principal // key value -> caller
}

func NewAPIKeyAuth(keys []KeyConfig) *APIKeyAuth {
	m := make(map[string]*Principal, len(keys))
	for _, k := range keys {
		m[k.Key] = newPrincipal(k.Name, k.Targets)
	}
	return &APIKeyAuth{keys: m}
}

// Middleware returns an HTTP middleware that rejects requests without a valid API key.
// On success it sets the key name in the request context for downstream use (logging, rate limiting).
func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(headerAPIKey)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key")
			return
		}

		p, ok := a.lookup(key)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		ctx := context.WithValue(r.Context(), contextKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// lookup uses constant-time comparison to prevent timing attacks.
func (a *APIKeyAuth) lookup(candidate string) (*Principal, bool) {
	for key, p := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			return p, true
		}
	}
	return nil, false
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
