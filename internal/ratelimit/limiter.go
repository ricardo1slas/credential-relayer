package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/rs/zerolog"
)

type Config struct {
	Requests int           `koanf:"requests"` // max requests per window
	Window   time.Duration `koanf:"window"`   // time window
}

// bucket tracks token state for one client.
type bucket struct {
	tokens    float64
	lastFill  time.Time
	mu        sync.Mutex
	rate      float64 // tokens per second
	maxTokens float64
}

// Limiter implements a per-client token bucket rate limiter.
type Limiter struct {
	buckets sync.Map // key name -> *bucket
	cfg     Config
}

func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg}
}

func (l *Limiter) getBucket(key string) *bucket {
	rate := float64(l.cfg.Requests) / l.cfg.Window.Seconds()
	maxTokens := float64(l.cfg.Requests)

	val, loaded := l.buckets.LoadOrStore(key, &bucket{
		tokens:    maxTokens,
		lastFill:  time.Now(),
		rate:      rate,
		maxTokens: maxTokens,
	})
	b := val.(*bucket)
	if loaded {
		return b
	}
	return b
}

// allow checks if the client can make a request, consuming one token if so.
// Returns (allowed, retryAfter).
func (l *Limiter) allow(key string) (bool, time.Duration) {
	b := l.getBucket(key)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastFill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Calculate how long until one token is available.
	retryAfter := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	return false, retryAfter
}

// StartEviction periodically removes buckets that haven't been touched in 10×window.
// Without this, the sync.Map grows unboundedly as unique keys (API-key names or IPs)
// show up -- a slow memory leak exploitable by an attacker rotating through IPs.
func (l *Limiter) StartEviction(ctx context.Context, logger zerolog.Logger) {
	sweep := l.cfg.Window * 2
	if sweep < 30*time.Second {
		sweep = 30 * time.Second
	}
	idleCutoff := l.cfg.Window * 10
	ticker := time.NewTicker(sweep)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-idleCutoff)
			evicted := 0
			l.buckets.Range(func(k, v any) bool {
				b := v.(*bucket)
				b.mu.Lock()
				stale := b.lastFill.Before(cutoff)
				b.mu.Unlock()
				if stale {
					l.buckets.Delete(k)
					evicted++
				}
				return true
			})
			if evicted > 0 {
				logger.Debug().Int("evicted", evicted).Msg("rate limit buckets evicted")
			}
		}
	}
}

// Middleware returns an HTTP middleware that rate limits requests per API key.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := auth.KeyName(r.Context())
		if key == "" {
			key = r.RemoteAddr // fallback if no auth
		}

		allowed, retryAfter := l.allow(key)
		if !allowed {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":       "rate limit exceeded",
				"retry_after": retryAfter.String(),
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
