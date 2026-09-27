package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordRelaySuccess(t *testing.T) {
	m := New()
	m.RecordRelay("svc-a", "k1", 200, 2, false, 150*time.Millisecond)

	if got := testutil.ToFloat64(m.relayRequests.WithLabelValues("svc-a", "success")); got != 1 {
		t.Errorf("relay success counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.relayErrors.WithLabelValues("svc-a")); got != 0 {
		t.Errorf("relay error counter = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.upstreamResponses.WithLabelValues("svc-a", "k1", "200")); got != 1 {
		t.Errorf("upstream responses{200} = %v, want 1", got)
	}
}

func TestRecordRelayFailureIncrementsErrors(t *testing.T) {
	m := New()
	m.RecordRelay("svc-b", "k1", 0, 4, true, time.Second)

	if got := testutil.ToFloat64(m.relayRequests.WithLabelValues("svc-b", "error")); got != 1 {
		t.Errorf("relay error outcome counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.relayErrors.WithLabelValues("svc-b")); got != 1 {
		t.Errorf("relay error counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.upstreamResponses.WithLabelValues("svc-b", "k1", "error")); got != 1 {
		t.Errorf("upstream responses{error} = %v, want 1", got)
	}
}

func TestRecordRejectionAndStream(t *testing.T) {
	m := New()
	m.RecordRejection("gemini", "demo", "path")
	m.RecordStream("gemini", "demo", 1500*time.Millisecond, 4*time.Second, 4096)
	m.RecordStream("gemini", "demo", 0, time.Second, 10) // no body: no TTFB sample

	if got := testutil.ToFloat64(m.rejections.WithLabelValues("gemini", "demo", "path")); got != 1 {
		t.Errorf("rejections = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.streamBytes.WithLabelValues("gemini", "demo")); got != 4106 {
		t.Errorf("stream bytes = %v, want 4106", got)
	}
	if n := testutil.CollectAndCount(m.streamTTFB); n != 1 {
		t.Errorf("ttfb series = %d, want 1", n)
	}
	if got := histogramCount(t, m, "relayer_stream_ttfb_seconds"); got != 1 {
		t.Errorf("ttfb samples = %d, want 1 (zero TTFB must not be observed)", got)
	}
	if got := histogramCount(t, m, "relayer_stream_duration_seconds"); got != 2 {
		t.Errorf("duration samples = %d, want 2", got)
	}
}

func histogramCount(t *testing.T, m *Metrics, name string) uint64 {
	t.Helper()
	mfs, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			var n uint64
			for _, mt := range mf.GetMetric() {
				n += mt.GetHistogram().GetSampleCount()
			}
			return n
		}
	}
	return 0
}

func TestRecordHTTPAndJobAndDepth(t *testing.T) {
	m := New()
	m.RecordHTTP(http.MethodPost, "/api/v1/relay", 200, 10*time.Millisecond)
	m.RecordJob("completed")
	m.RecordJob("dead")
	m.SetQueueDepth(7)

	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("POST", "/api/v1/relay", "200")); got != 1 {
		t.Errorf("http counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.jobsProcessed.WithLabelValues("completed")); got != 1 {
		t.Errorf("jobs completed = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.queueDepth); got != 7 {
		t.Errorf("queue depth = %v, want 7", got)
	}
}

func TestHandlerExposesMetrics(t *testing.T) {
	m := New()
	m.RecordRelay("svc-a", "k1", 200, 1, false, time.Millisecond)

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "relayer_relay_requests_total") {
		t.Errorf("metrics output missing relayer_relay_requests_total:\n%s", body)
	}
}

// A nil *Metrics must be safe to call -- components hold the interface and may
// be constructed with metrics disabled.
func TestNilMetricsIsSafe(t *testing.T) {
	var m *Metrics
	m.RecordHTTP("GET", "/x", 200, 0)
	m.RecordRelay("t", "k", 200, 1, false, 0)
	m.RecordRejection("t", "k", "scope")
	m.RecordStream("t", "k", time.Second, 2*time.Second, 10)
	m.RecordJob("completed")
	m.SetQueueDepth(1)
	if m.Handler() == nil {
		t.Error("nil Metrics Handler() returned nil")
	}
}
