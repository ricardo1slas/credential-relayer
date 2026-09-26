package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof" // #nosec G108 -- registered on the private metrics mux only, opt-in via metrics.pprof
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ricardo1slas/private_relayer/internal/api"
	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/ricardo1slas/private_relayer/internal/config"
	"github.com/ricardo1slas/private_relayer/internal/metrics"
	"github.com/ricardo1slas/private_relayer/internal/queue"
	"github.com/ricardo1slas/private_relayer/internal/ratelimit"
	"github.com/ricardo1slas/private_relayer/internal/relay"
	"github.com/rs/zerolog"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load config")
	}

	logger.Info().
		Int("port", cfg.Server.Port).
		Dur("read_timeout", cfg.Server.ReadTimeout).
		Dur("write_timeout", cfg.Server.WriteTimeout).
		Int64("max_request_bytes", cfg.Relay.MaxRequestBytes).
		Int64("max_response_bytes", cfg.Relay.MaxResponseBytes).
		Bool("preserve_client_identity", cfg.Relay.PreserveClientIdentity).
		Msg("starting relayer")

	// Build target registry from config.
	relayTargets := make([]relay.TargetConfig, len(cfg.Relay.Targets))
	for i, t := range cfg.Relay.Targets {
		relayTargets[i] = relay.TargetConfig{
			Name:              t.Name,
			URL:               t.URL,
			Timeout:           t.Timeout,
			Retries:           t.Retries,
			AllowHTTP:         t.AllowHTTP,
			AllowPrivateIPs:   t.AllowPrivateIPs,
			PreserveUserAgent: t.PreserveUserAgent,
			SecretHeaders:     t.SecretHeaders,
			AllowMethods:      t.AllowMethods,
			AllowPaths:        t.AllowPaths,
			MaxBodyBytes:      t.MaxBodyBytes,
		}
	}

	registry, err := relay.NewTargetRegistry(relayTargets)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create target registry")
	}

	// Build auth middleware from config.
	authKeys := make([]auth.KeyConfig, len(cfg.Auth.APIKeys))
	for i, k := range cfg.Auth.APIKeys {
		authKeys[i] = auth.KeyConfig{Name: k.Name, Key: k.Key, KeySHA256: k.KeySHA256, ExpiresAt: k.ExpiresAt, Targets: k.Targets}
		if err := authKeys[i].Validate(); err != nil {
			logger.Fatal().Err(err).Msg("invalid api key config")
		}
		if k.Key != "" {
			logger.Warn().Str("key", k.Name).Msg("api key is stored in plaintext: use key_sha256 (make new-key)")
		}
		if len(k.Targets) == 0 {
			logger.Warn().Str("key", k.Name).Msg("api key has no targets list: it can use every target (set targets: [...] to scope it)")
		}
		if exp, ok, _ := authKeys[i].Expiry(); ok && time.Until(exp) < 14*24*time.Hour {
			logger.Warn().Str("key", k.Name).Time("expires_at", exp).Msg("api key expires within 14 days")
		}
	}
	apiKeyAuth := auth.NewAPIKeyAuth(authKeys).WithLogger(logger)

	// Build rate limiter from config.
	limiter := ratelimit.New(ratelimit.Config{
		Requests: cfg.RateLimit.Requests,
		Window:   cfg.RateLimit.Window,
	})

	// Connect to Redis.
	redisClient, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Queue.Redis.Addr,
		Password: cfg.Queue.Redis.Password,
		DB:       cfg.Queue.Redis.DB,
		TLS:      cfg.Queue.Redis.TLS,
	}, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to redis")
	}

	// Metrics: a private Prometheus registry shared across the relay handler,
	// worker pool and HTTP middleware. Disabled cleanly when metrics.enabled=false.
	var m *metrics.Metrics
	if cfg.Metrics.Enabled {
		m = metrics.New()
	}

	// Build queue and worker pool.
	jobQueue := queue.NewQueue(redisClient, queue.Config{
		CompletedTTL: cfg.Queue.CompletedTTL,
		FailedTTL:    cfg.Queue.FailedTTL,
	}, logger)
	relayHandler := relay.NewHandler(registry, relay.HandlerConfig{
		MaxRequestBytes:        cfg.Relay.MaxRequestBytes,
		MaxResponseBytes:       cfg.Relay.MaxResponseBytes,
		PreserveClientIdentity: cfg.Relay.PreserveClientIdentity,
		UserAgent:              cfg.Relay.UserAgent,
		MaxRedirects:           cfg.Relay.MaxRedirects,
	}, logger)
	relayHandler.SetMetrics(m)

	workerCfg := queue.WorkerPoolConfig{
		Workers:    cfg.Queue.Workers,
		RetryDelay: cfg.Queue.RetryDelay,
	}
	workerPool := queue.NewWorkerPool(jobQueue, relayHandler, workerCfg, logger)
	workerPool.SetMetrics(m)

	router := api.NewRouter(api.RouterDeps{
		Logger:          logger,
		RelayHandler:    relayHandler,
		APIKeyAuth:      apiKeyAuth,
		Limiter:         limiter,
		Queue:           jobQueue,
		RedisClient:     redisClient,
		Metrics:         m,
		MaxRequestBytes: cfg.Relay.MaxRequestBytes,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           router,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       60 * time.Second,
	}

	// Start worker pool + rate-limit sweeper in background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go workerPool.Start(ctx)
	go limiter.StartEviction(ctx, logger)

	// Metrics: serve /metrics on a dedicated port (kept off the public ALB listener,
	// which only forwards :8080) and poll queue depth into the gauge.
	var metricsSrv *http.Server
	if m != nil {
		mux := http.NewServeMux()
		mux.Handle("/metrics", m.Handler())
		if cfg.Metrics.Pprof {
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			logger.Warn().Msg("pprof enabled on metrics port")
		}
		metricsSrv = &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Metrics.Port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			logger.Info().Msgf("metrics listening on :%d/metrics", cfg.Metrics.Port)
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error().Err(err).Msg("metrics server error")
			}
		}()
		go pollQueueDepth(ctx, jobQueue, m, logger)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info().Str("signal", sig.String()).Msg("shutting down")
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		if metricsSrv != nil {
			_ = metricsSrv.Shutdown(shutdownCtx)
		}
	}()

	logger.Info().Msgf("listening on :%d", cfg.Server.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server error")
	}
}

// pollQueueDepth refreshes the queue-depth gauge on a fixed interval. A short
// timeout per probe keeps a slow/unreachable Redis from blocking the ticker.
func pollQueueDepth(ctx context.Context, q *queue.Queue, m *metrics.Metrics, logger zerolog.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			depth, err := q.Depth(probeCtx)
			cancel()
			if err != nil {
				logger.Debug().Err(err).Msg("queue depth probe failed")
				continue
			}
			m.SetQueueDepth(depth)
		}
	}
}
