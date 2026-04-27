package queue

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	TLS      bool
}

func NewRedisClient(cfg RedisConfig, logger zerolog.Logger) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connecting to redis at %s: %w", cfg.Addr, err)
	}

	logger.Info().Str("addr", cfg.Addr).Msg("connected to redis")
	return client, nil
}

// HealthCheck returns nil if Redis is reachable, an error otherwise.
func HealthCheck(ctx context.Context, client *redis.Client) error {
	return client.Ping(ctx).Err()
}
