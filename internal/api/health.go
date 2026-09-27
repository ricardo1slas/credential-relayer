package api

import (
	"encoding/json"
	"net/http"

	"github.com/redis/go-redis/v9"
	"github.com/ricardo1slas/credential-relayer/internal/queue"
)

func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	}
}

func readyHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if redisClient != nil {
			if err := queue.HealthCheck(r.Context(), redisClient); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "not ready",
					"error":  "redis: " + err.Error(),
				})
				return
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ready",
		})
	}
}
