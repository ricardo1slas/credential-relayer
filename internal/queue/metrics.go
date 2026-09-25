package queue

// Recorder is the metrics surface the worker pool needs. Kept as an interface so
// the queue package doesn't depend on internal/metrics. *metrics.Metrics
// satisfies it.
type Recorder interface {
	RecordJob(status string)
}

type nopRecorder struct{}

func (nopRecorder) RecordJob(string) {}
