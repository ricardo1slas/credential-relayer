package queue

import (
	"encoding/json"
	"time"
)

type JobStatus string

const (
	StatusQueued     JobStatus = "queued"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
	StatusDead       JobStatus = "dead"
)

type Job struct {
	ID           string            `json:"id"`
	Owner        string            `json:"owner,omitempty"` // API key name that enqueued it
	Target       string            `json:"target"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         json.RawMessage   `json:"body,omitempty"`
	Status       JobStatus         `json:"status"`
	Attempts     int               `json:"attempts"`
	MaxRetries   int               `json:"max_retries"`
	Error        string            `json:"error,omitempty"`
	ResponseCode int               `json:"response_code,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func (j *Job) Marshal() ([]byte, error) {
	return json.Marshal(j)
}

func UnmarshalJob(data []byte) (*Job, error) {
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	return &j, nil
}
