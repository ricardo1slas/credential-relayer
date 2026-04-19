package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ricardo1slas/private_relayer/internal/api"
	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/ricardo1slas/private_relayer/internal/config"
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
		Msg("starting relayer")

	// Build target registry from config.
	relayTargets := make([]relay.TargetConfig, len(cfg.Relay.Targets))
	for i, t := range cfg.Relay.Targets {
		relayTargets[i] = relay.TargetConfig{
			Name:    t.Name,
			URL:     t.URL,
			Timeout: t.Timeout,
			Retries: t.Retries,
		}
	}

	registry, err := relay.NewTargetRegistry(relayTargets)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create target registry")
	}

	// Build auth middleware from config.
	authKeys := make([]auth.KeyConfig, len(cfg.Auth.APIKeys))
	for i, k := range cfg.Auth.APIKeys {
		authKeys[i] = auth.KeyConfig{Name: k.Name, Key: k.Key}
	}
	apiKeyAuth := auth.NewAPIKeyAuth(authKeys)

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
	}, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to redis")
	}

	// Build queue and worker pool.
	jobQueue := queue.NewQueue(redisClient, logger)
	relayHandler := relay.NewHandler(registry, logger)

	workerCfg := queue.WorkerPoolConfig{
		Workers:    cfg.Queue.Workers,
		RetryDelay: cfg.Queue.RetryDelay,
	}
	workerPool := queue.NewWorkerPool(jobQueue, relayHandler, workerCfg, logger)

	router := api.NewRouter(api.RouterDeps{
		Logger:       logger,
		RelayHandler: relayHandler,
		APIKeyAuth:   apiKeyAuth,
		Limiter:      limiter,
		Queue:        jobQueue,
		RedisClient:  redisClient,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	// Start worker pool in background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go workerPool.Start(ctx)

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
	}()

	logger.Info().Msgf("listening on :%d", cfg.Server.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server error")
	}
}
