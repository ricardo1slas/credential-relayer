package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"github.com/ricardo1slas/credential-relayer/internal/metrics"
	"github.com/rs/zerolog"
)

// Limiter decides whether key may make one more request. Memory keeps buckets in
// process; Redis shares them across every instance behind the load balancer.
type Limiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

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

// Memory implements a per-client token bucket rate limiter in process memory.
// Each instance has its own buckets, so N instances allow N times the limit.
type Memory struct {
	buckets sync.Map // key name -> *bucket
	cfg     Config
}

func NewMemory(cfg Config) *Memory {
	return &Memory{cfg: cfg}
}

func (l *Memory) getBucket(key string) *bucket {
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

// Allow checks if the client can make a request, consuming one token if so.
// It never returns an error.
func (l *Memory) Allow(_ context.Context, key string) (bool, time.Duration, error) {
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
		return true, 0, nil
	}

	// Calculate how long until one token is available.
	retryAfter := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	return false, retryAfter, nil
}

// StartEviction periodically removes buckets that haven't been touched in 10×window.
// Without this, the sync.Map grows unboundedly as unique keys (API-key names or IPs)
// show up -- a slow memory leak exploitable by an attacker rotating through IPs.
func (l *Memory) StartEviction(ctx context.Context, logger zerolog.Logger) {
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
// If the backend errors, failOpen lets the request through; otherwise it is refused
// with 503. Not 429: the client did nothing wrong, and a 429 would tell it to back
// off as if it had spent its budget.
func Middleware(l Limiter, failOpen bool, logger zerolog.Logger, m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := auth.KeyName(r.Context())
			if key == "" {
				key = r.RemoteAddr // fallback if no auth
			}

			allowed, retryAfter, err := l.Allow(r.Context(), key)
			if err != nil {
				decision := "denied"
				if failOpen {
					decision = "allowed"
				}
				logger.Warn().Err(err).Str("decision", decision).Msg("rate limit backend error")
				m.RecordRateLimitBackendError(decision)
				if failOpen {
					next.ServeHTTP(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limiter unavailable"})
				return
			}
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
}
