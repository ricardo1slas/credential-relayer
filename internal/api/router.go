package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"github.com/ricardo1slas/credential-relayer/internal/metrics"
	"github.com/ricardo1slas/credential-relayer/internal/queue"
	"github.com/ricardo1slas/credential-relayer/internal/ratelimit"
	"github.com/ricardo1slas/credential-relayer/internal/relay"
	"github.com/rs/zerolog"
)

type RouterDeps struct {
	Logger          zerolog.Logger
	RelayHandler    *relay.Handler
	APIKeyAuth      *auth.APIKeyAuth
	Limiter         *ratelimit.Limiter
	Queue           *queue.Queue
	RedisClient     *redis.Client
	Metrics         *metrics.Metrics
	MaxRequestBytes int64
}

func NewRouter(deps RouterDeps) chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// No RealIP: chi deprecated it as spoofable (X-Forwarded-For forgery). The Pi has no proxy in
	// front, so RemoteAddr is the true peer. Behind an ALB use middleware.ClientIPFromXFFTrustedProxies.
	r.Use(requestLogger(deps.Logger))
	r.Use(metricsMiddleware(deps.Metrics))
	r.Use(middleware.Recoverer)

	// Health endpoints -- no auth, no rate limiting.
	r.Get("/health", healthHandler())
	r.Get("/ready", readyHandler(deps.RedisClient))

	// API routes -- auth + rate limiting.
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(deps.APIKeyAuth.Middleware)
		r.Use(deps.Limiter.Middleware)
		r.Post("/relay", deps.RelayHandler.ServeHTTP)
		r.Post("/webhook", webhookHandler(deps.Queue, deps.RelayHandler, deps.MaxRequestBytes))
		r.Get("/jobs/{id}", jobStatusHandler(deps.Queue))
		r.HandleFunc("/proxy/{target}/*", proxyHandler(deps.RelayHandler))
	})

	return r
}
