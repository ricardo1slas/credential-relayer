package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ricardo1slas/private_relayer/internal/metrics"
	"github.com/rs/zerolog"
)

// metricsMiddleware records request count and latency per (method, route pattern,
// status). It keys on the chi route pattern, not the concrete path, so high-
// cardinality IDs (/jobs/{id}) never explode the metric label set. A nil *Metrics
// makes this a transparent pass-through.
func metricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m == nil {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			m.RecordHTTP(r.Method, route, status, time.Since(start))
		})
	}
}

func requestLogger(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				// Log the route *pattern* (e.g. "/api/v1/jobs/{id}") not the concrete
				// path ("/api/v1/jobs/a1b2-...") so that IDs or PII in URLs never
				// land in logs. Falls back to the raw path if no pattern matched
				// (e.g. 404s before routing resolved).
				routePath := chi.RouteContext(r.Context()).RoutePattern()
				if routePath == "" {
					routePath = r.URL.Path
				}
				logger.Info().
					Str("method", r.Method).
					Str("route", routePath).
					Int("status", ww.Status()).
					Dur("latency", time.Since(start)).
					Str("request_id", middleware.GetReqID(r.Context())).
					Msg("request completed")
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
