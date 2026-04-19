package auth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// #nosec G101 -- this is the header *name*, not a credential value.
const headerAPIKey = "X-API-Key"

type KeyConfig struct {
	Name string `koanf:"name"`
	Key  string `koanf:"key"`
}

type APIKeyAuth struct {
	keys map[string]string // key value -> key name
}

func NewAPIKeyAuth(keys []KeyConfig) *APIKeyAuth {
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		m[k.Key] = k.Name
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

		name, ok := a.lookup(key)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		ctx := withKeyName(r.Context(), name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// lookup uses constant-time comparison to prevent timing attacks.
func (a *APIKeyAuth) lookup(candidate string) (string, bool) {
	for key, name := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			return name, true
		}
	}
	return "", false
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
