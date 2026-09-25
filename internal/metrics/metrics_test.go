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
	m.RecordRelay("svc-a", 200, 2, false, 150*time.Millisecond)

	if got := testutil.ToFloat64(m.relayRequests.WithLabelValues("svc-a", "success")); got != 1 {
		t.Errorf("relay success counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.relayErrors.WithLabelValues("svc-a")); got != 0 {
		t.Errorf("relay error counter = %v, want 0", got)
	}
}

func TestRecordRelayFailureIncrementsErrors(t *testing.T) {
	m := New()
	m.RecordRelay("svc-b", 0, 4, true, time.Second)

	if got := testutil.ToFloat64(m.relayRequests.WithLabelValues("svc-b", "error")); got != 1 {
		t.Errorf("relay error outcome counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.relayErrors.WithLabelValues("svc-b")); got != 1 {
		t.Errorf("relay error counter = %v, want 1", got)
	}
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
	m.RecordRelay("svc-a", 200, 1, false, time.Millisecond)

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
	m.RecordRelay("t", 200, 1, false, 0)
	m.RecordJob("completed")
	m.SetQueueDepth(1)
	if m.Handler() == nil {
		t.Error("nil Metrics Handler() returned nil")
	}
}
