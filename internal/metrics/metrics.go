// Package metrics holds the Prometheus instrumentation for the relayer.
//
// It is instance-based (a *Metrics carries its own *prometheus.Registry) rather
// than using the global default registry. That mirrors the rest of the codebase
// (koanf config, no global singletons) and makes the collectors safe to create
// fresh in every unit test without "duplicate metrics collector registration"
// panics.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the full set of collectors exposed by the service. Construct one
// with New and pass it to the components that need to record (api router, relay
// handler, worker pool). A nil *Metrics is safe: every method no-ops, so callers
// never have to nil-check.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	relayRequests *prometheus.CounterVec
	relayErrors   *prometheus.CounterVec
	relayDuration *prometheus.HistogramVec
	relayAttempts *prometheus.HistogramVec

	jobsProcessed *prometheus.CounterVec
	queueDepth    prometheus.Gauge

	// Per-key / per-target views for the security and LLM dashboards. Label values are
	// bounded: key names come from config, targets from the registry ("unknown" otherwise).
	upstreamResponses *prometheus.CounterVec
	rejections        *prometheus.CounterVec
	streamTTFB        *prometheus.HistogramVec
	streamDuration    *prometheus.HistogramVec
	streamBytes       *prometheus.CounterVec
}

// New builds and registers all collectors against a private registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_http_requests_total",
			Help: "Total HTTP requests handled, labelled by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relayer_http_request_duration_seconds",
			Help:    "HTTP request handling latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		relayRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_relay_requests_total",
			Help: "Total relay attempts to upstream targets, labelled by target and outcome.",
		}, []string{"target", "outcome"}),
		relayErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_relay_errors_total",
			Help: "Total relay attempts that ended in a transport/exhausted-retries error.",
		}, []string{"target"}),
		relayDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relayer_relay_duration_seconds",
			Help:    "End-to-end relay duration in seconds (includes retries), labelled by target.",
			Buckets: prometheus.DefBuckets,
		}, []string{"target"}),
		relayAttempts: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relayer_relay_attempts",
			Help:    "Number of attempts made per relay, labelled by target.",
			Buckets: []float64{1, 2, 3, 4, 5, 6, 8, 10},
		}, []string{"target"}),
		jobsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_jobs_processed_total",
			Help: "Total async jobs processed by the worker pool, labelled by terminal status.",
		}, []string{"status"}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "relayer_queue_depth",
			Help: "Number of jobs currently waiting in the pending queue.",
		}),
		upstreamResponses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_upstream_responses_total",
			Help: "Final upstream outcome per relay/proxy call: HTTP status code, or \"error\" when no response.",
		}, []string{"target", "key", "code"}),
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_rejections_total",
			Help: "Requests refused before reaching the upstream, by reason (scope, method, path, body, not_found, bad_path, bad_method).",
		}, []string{"target", "key", "reason"}),
		streamTTFB: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relayer_stream_ttfb_seconds",
			Help:    "Proxy: time from request to the first response body byte sent to the client.",
			Buckets: []float64{.1, .25, .5, 1, 2, 3, 5, 8, 13, 20, 30, 60},
		}, []string{"target"}),
		streamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relayer_stream_duration_seconds",
			Help:    "Proxy: total time until the response stream ended.",
			Buckets: []float64{.5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600},
		}, []string{"target"}),
		streamBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relayer_stream_bytes_total",
			Help: "Proxy: response bytes streamed to clients.",
		}, []string{"target", "key"}),
	}

	reg.MustRegister(
		m.httpRequests, m.httpDuration,
		m.relayRequests, m.relayErrors, m.relayDuration, m.relayAttempts,
		m.jobsProcessed, m.queueDepth,
		m.upstreamResponses, m.rejections, m.streamTTFB, m.streamDuration, m.streamBytes,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// Handler returns the promhttp handler that serves the registry at /metrics.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry exposes the underlying registry (used by tests to gather directly).
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.registry
}

// RecordHTTP records one served HTTP request.
func (m *Metrics) RecordHTTP(method, route string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// RecordRelay records the outcome of a single relay (HTTP, worker or proxy path) made on
// behalf of key. statusCode is the upstream status (0 when the relay errored before a response).
func (m *Metrics) RecordRelay(target, key string, statusCode, attempts int, failed bool, d time.Duration) {
	if m == nil {
		return
	}
	outcome := "success"
	code := strconv.Itoa(statusCode)
	if failed || statusCode == 0 {
		code = "error"
	}
	if failed {
		outcome = "error"
		m.relayErrors.WithLabelValues(target).Inc()
	}
	m.upstreamResponses.WithLabelValues(target, key, code).Inc()
	m.relayRequests.WithLabelValues(target, outcome).Inc()
	m.relayDuration.WithLabelValues(target).Observe(d.Seconds())
	if attempts > 0 {
		m.relayAttempts.WithLabelValues(target).Observe(float64(attempts))
	}
}

// RecordJob records a worker job reaching a terminal status (completed/dead).
func (m *Metrics) RecordJob(status string) {
	if m == nil {
		return
	}
	m.jobsProcessed.WithLabelValues(status).Inc()
}

// SetQueueDepth updates the pending-queue depth gauge.
func (m *Metrics) SetQueueDepth(n int64) {
	if m == nil {
		return
	}
	m.queueDepth.Set(float64(n))
}

// RecordRejection records a request refused before anything was sent upstream.
func (m *Metrics) RecordRejection(target, key, reason string) {
	if m == nil {
		return
	}
	m.rejections.WithLabelValues(target, key, reason).Inc()
}

// RecordStream records one proxied response stream.
func (m *Metrics) RecordStream(target, key string, ttfb, total time.Duration, bytes int64) {
	if m == nil {
		return
	}
	if ttfb > 0 {
		m.streamTTFB.WithLabelValues(target).Observe(ttfb.Seconds())
	}
	m.streamDuration.WithLabelValues(target).Observe(total.Seconds())
	m.streamBytes.WithLabelValues(target, key).Add(float64(bytes))
}
