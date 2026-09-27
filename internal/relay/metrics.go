package relay

import "time"

// Recorder is the slice of the metrics surface the relay handler needs. Keeping
// it as an interface here (rather than importing internal/metrics) means the
// relay package stays dependency-free and the recorder is trivially faked in
// tests. *metrics.Metrics satisfies this.
type Recorder interface {
	RecordRelay(target, key string, statusCode, attempts int, failed bool, d time.Duration)
	RecordRejection(target, key, reason string)
	RecordStream(target, key string, ttfb, total time.Duration, bytes int64)
}

// nopRecorder is the default so callers never have to nil-check.
type nopRecorder struct{}

func (nopRecorder) RecordRelay(string, string, int, int, bool, time.Duration)        {}
func (nopRecorder) RecordRejection(string, string, string)                           {}
func (nopRecorder) RecordStream(string, string, time.Duration, time.Duration, int64) {}
