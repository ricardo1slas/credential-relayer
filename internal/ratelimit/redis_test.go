package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/ricardo1slas/credential-relayer/internal/metrics"
	"github.com/rs/zerolog"
)

func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{Addr: addr, DB: 14}) // not 15: queue/e2e tests FlushDB 15 while packages run in parallel
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available at %s: %v", addr, err)
	}

	client.FlushDB(ctx)
	t.Cleanup(func() {
		client.FlushDB(context.Background())
		_ = client.Close()
	})
	return client
}

func TestRedisLimiter_AllowDeny(t *testing.T) {
	l := NewRedis(testRedisClient(t), Config{Requests: 3, Window: time.Minute})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, _, err := l.Allow(ctx, "k")
		if err != nil || !ok {
			t.Fatalf("request %d: allowed=%v err=%v", i+1, ok, err)
		}
	}
	ok, retryAfter, err := l.Allow(ctx, "k")
	if err != nil || ok {
		t.Fatalf("4th request: allowed=%v err=%v, want denied", ok, err)
	}
	if retryAfter <= 0 || retryAfter > 20*time.Second {
		t.Errorf("retryAfter = %v, want ~20s", retryAfter)
	}
	if ok, _, _ := l.Allow(ctx, "other"); !ok {
		t.Error("other key should have its own budget")
	}
}

// Two instances (separate clients, same Redis) must share one budget: this is the
// bug the in-memory limiter has behind a load balancer.
func TestRedisLimiter_SharedAcrossInstances(t *testing.T) {
	cfg := Config{Requests: 4, Window: time.Minute}
	a := NewRedis(testRedisClient(t), cfg)
	b := NewRedis(testRedisClient(t), cfg)
	ctx := context.Background()

	allowed := 0
	for i := 0; i < 10; i++ {
		l := a
		if i%2 == 1 {
			l = b
		}
		ok, _, err := l.Allow(ctx, "shared")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 4 {
		t.Fatalf("allowed = %d across two instances, want 4", allowed)
	}
}

func TestRedisLimiter_KeysExpire(t *testing.T) {
	client := testRedisClient(t)
	l := NewRedis(client, Config{Requests: 5, Window: time.Minute})
	if _, _, err := l.Allow(context.Background(), "idle"); err != nil {
		t.Fatal(err)
	}
	ttl := client.PTTL(context.Background(), keyPrefix+"idle").Val()
	if ttl <= 0 || ttl > time.Minute {
		t.Fatalf("ttl = %v, want (0, 1m]", ttl)
	}
}

type errLimiter struct{}

func (errLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}

func TestMiddleware_BackendError(t *testing.T) {
	for _, tc := range []struct {
		failOpen bool
		code     int
		decision string
	}{
		{failOpen: false, code: http.StatusServiceUnavailable, decision: "denied"},
		{failOpen: true, code: http.StatusOK, decision: "allowed"},
	} {
		m := metrics.New()
		h := Middleware(errLimiter{}, tc.failOpen, zerolog.Nop(), m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != tc.code {
			t.Errorf("fail_open=%v: code = %d, want %d", tc.failOpen, rec.Code, tc.code)
		}
		want := `
# HELP relayer_ratelimit_backend_errors_total Rate limit checks that failed in the backend, by what the request got (allowed = fail-open, denied = fail-closed).
# TYPE relayer_ratelimit_backend_errors_total counter
relayer_ratelimit_backend_errors_total{decision="` + tc.decision + `"} 1
`
		if err := testutil.GatherAndCompare(m.Registry(), strings.NewReader(want), "relayer_ratelimit_backend_errors_total"); err != nil {
			t.Errorf("fail_open=%v: %v", tc.failOpen, err)
		}
	}
}

// A real client pointed at a closed port must surface an error quickly, not hang.
func TestRedisLimiter_UnreachableErrors(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = client.Close() }()
	l := NewRedis(client, Config{Requests: 1, Window: time.Minute})

	start := time.Now()
	if _, _, err := l.Allow(context.Background(), "k"); err == nil {
		t.Fatal("expected an error from an unreachable redis")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Allow took %v; the per-call timeout should bound it", d)
	}
}
