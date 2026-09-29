package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// #nosec G101 -- this is the header *name*, not a credential value.
const (
	headerAPIKey     = "X-API-Key"
	headerGoogAPIKey = "X-Goog-Api-Key"
)

// KeyConfig describes one API key. Set exactly one of Key (plaintext, dev only) or
// KeySHA256 (hex SHA-256 of the key; the config then holds no usable secret).
// API keys are high-entropy random strings (make new-key), so a fast hash is appropriate;
// a password KDF is only needed for low-entropy secrets.
type KeyConfig struct {
	Name      string   `koanf:"name"`
	Key       string   `koanf:"key"`
	KeySHA256 string   `koanf:"key_sha256"`
	ExpiresAt string   `koanf:"expires_at"` // optional: RFC 3339 or YYYY-MM-DD (end of that day, UTC)
	Targets   []string `koanf:"targets"`    // allowed relay targets; empty or ["*"] = all
}

// MinKeyLength is the shortest plaintext key accepted. Keys are hashed with a fast hash (SHA-256),
// which is only safe for high-entropy secrets: a short, guessable key could be brute-forced from its
// hash. make new-key produces 64 hex characters (256 bits). Hashed keys can't be measured, so the
// guarantee there rests on generating them with make new-key.
const MinKeyLength = 32

// Validate rejects ambiguous or malformed key entries so a typo can't silently weaken auth.
func (k KeyConfig) Validate() error {
	if k.Name == "" {
		return fmt.Errorf("api key without a name")
	}
	if (k.Key == "") == (k.KeySHA256 == "") {
		return fmt.Errorf("key %q: set exactly one of key or key_sha256", k.Name)
	}
	if k.Key != "" && len(k.Key) < MinKeyLength {
		return fmt.Errorf("key %q: plaintext keys must be at least %d characters (generate one with make new-key)", k.Name, MinKeyLength)
	}
	if k.KeySHA256 != "" {
		if b, err := hex.DecodeString(k.KeySHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("key %q: key_sha256 must be 64 hex characters", k.Name)
		}
	}
	if _, _, err := k.Expiry(); err != nil {
		return fmt.Errorf("key %q: %w", k.Name, err)
	}
	return nil
}

// Expiry returns the parsed expiry, or ok=false when the key never expires.
func (k KeyConfig) Expiry() (t time.Time, ok bool, err error) {
	if k.ExpiresAt == "" {
		return time.Time{}, false, nil
	}
	if t, err = time.Parse(time.RFC3339, k.ExpiresAt); err == nil {
		return t, true, nil
	}
	if t, err = time.Parse("2006-01-02", k.ExpiresAt); err == nil {
		return t.Add(24*time.Hour - time.Nanosecond), true, nil
	}
	return time.Time{}, false, fmt.Errorf("expires_at %q is not RFC 3339 or YYYY-MM-DD", k.ExpiresAt)
}

type keyEntry struct {
	digest    [sha256.Size]byte
	principal *Principal
	expires   time.Time // zero = never
}

type APIKeyAuth struct {
	entries []keyEntry
	logger  zerolog.Logger
	now     func() time.Time
}

// NewAPIKeyAuth builds the authenticator. Call KeyConfig.Validate on each entry first.
// Several entries may share a name: that is how keys are rotated with an overlap window.
func NewAPIKeyAuth(keys []KeyConfig) *APIKeyAuth {
	a := &APIKeyAuth{logger: zerolog.Nop(), now: time.Now}
	for _, k := range keys {
		e := keyEntry{principal: newPrincipal(k.Name, k.Targets)}
		if k.KeySHA256 != "" {
			b, _ := hex.DecodeString(k.KeySHA256)
			copy(e.digest[:], b)
		} else {
			e.digest = sha256.Sum256([]byte(k.Key))
		}
		if t, ok, _ := k.Expiry(); ok {
			e.expires = t
		}
		a.entries = append(a.entries, e)
	}
	return a
}

// WithLogger enables security logging (expired-key use). Never logs key material.
func (a *APIKeyAuth) WithLogger(l zerolog.Logger) *APIKeyAuth {
	a.logger = l
	return a
}

// Middleware returns an HTTP middleware that rejects requests without a valid, unexpired API key.
// On success it sets the caller in the request context (logging, rate limiting, scopes).
func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(headerAPIKey)
		if key == "" {
			// Google SDKs (Gemini CLI via GOOGLE_GEMINI_BASE_URL) send their key here; the proxy
			// never forwards it upstream (relay.proxyDropRequest).
			key = r.Header.Get(headerGoogAPIKey)
		}
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key")
			return
		}

		p, ok := a.lookup(key)
		if !ok {
			// Expired and unknown keys get the same response: no oracle for valid-but-expired keys.
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		ctx := context.WithValue(r.Context(), contextKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// lookup hashes the candidate and compares digests in constant time across every entry, so
// neither timing nor early exit reveals which (or how many) keys exist.
func (a *APIKeyAuth) lookup(candidate string) (*Principal, bool) {
	sum := sha256.Sum256([]byte(candidate))
	var match *keyEntry
	for i := range a.entries {
		if subtle.ConstantTimeCompare(sum[:], a.entries[i].digest[:]) == 1 {
			match = &a.entries[i]
		}
	}
	if match == nil {
		return nil, false
	}
	if !match.expires.IsZero() && a.now().After(match.expires) {
		a.logger.Warn().Str("key", match.principal.Name).Time("expired_at", match.expires).Msg("expired API key used")
		return nil, false
	}
	return match.principal, true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
