package queue

import (
	"context"
	"fmt"
	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const (
	queueKey      = "relayer:jobs:pending"
	deadLetterKey = "relayer:jobs:dead"
	jobPrefix     = "relayer:job:"

	defaultCompletedTTL = time.Hour
	defaultFailedTTL    = 24 * time.Hour
)

type Queue struct {
	client       *redis.Client
	logger       zerolog.Logger
	completedTTL time.Duration
	failedTTL    time.Duration
}

type Config struct {
	CompletedTTL time.Duration
	FailedTTL    time.Duration
}

func NewQueue(client *redis.Client, cfg Config, logger zerolog.Logger) *Queue {
	if cfg.CompletedTTL == 0 {
		cfg.CompletedTTL = defaultCompletedTTL
	}
	if cfg.FailedTTL == 0 {
		cfg.FailedTTL = defaultFailedTTL
	}
	return &Queue{
		client:       client,
		logger:       logger,
		completedTTL: cfg.CompletedTTL,
		failedTTL:    cfg.FailedTTL,
	}
}

// Enqueue creates a new job and pushes it to the pending queue.
// Callers are expected to have already scrubbed sensitive headers; see
// relay.StripSensitiveHeaders and webhook.go.
func (q *Queue) Enqueue(ctx context.Context, target, method, path string, headers map[string]string, body []byte, maxRetries int) (*Job, error) {
	job := &Job{
		ID:         uuid.New().String(),
		Owner:      auth.KeyName(ctx), // ctx carries the authenticated key from the HTTP request
		Target:     target,
		Method:     method,
		Path:       path,
		Headers:    headers,
		Body:       body,
		Status:     StatusQueued,
		MaxRetries: maxRetries,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	data, err := job.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshaling job: %w", err)
	}

	pipe := q.client.Pipeline()
	pipe.Set(ctx, jobPrefix+job.ID, data, q.failedTTL)
	pipe.LPush(ctx, queueKey, job.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("enqueueing job: %w", err)
	}

	q.logger.Info().Str("job_id", job.ID).Str("target", target).Msg("job enqueued")
	return job, nil
}

// Dequeue blocks until a job is available or the context is cancelled.
func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) (*Job, error) {
	result, err := q.client.BRPop(ctx, timeout, queueKey).Result()
	if err != nil {
		return nil, err
	}

	jobID := result[1]
	return q.GetJob(ctx, jobID)
}

// GetJob retrieves a job by ID.
func (q *Queue) GetJob(ctx context.Context, id string) (*Job, error) {
	data, err := q.client.Get(ctx, jobPrefix+id).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("job %s not found", id)
		}
		return nil, fmt.Errorf("getting job %s: %w", id, err)
	}
	return UnmarshalJob(data)
}

// UpdateJob persists the current job state with the long (failed_ttl) TTL.
// Use MarkCompleted for successful jobs -- it clears the body and applies the shorter TTL.
func (q *Queue) UpdateJob(ctx context.Context, job *Job) error {
	job.UpdatedAt = time.Now()
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}
	return q.client.Set(ctx, jobPrefix+job.ID, data, q.failedTTL).Err()
}

// MarkCompleted finalizes a successful job: clears the request body and headers
// (they served their purpose and shouldn't linger in Redis), sets status, and applies
// the shorter completed_ttl so metadata is still pollable by clients for a while.
func (q *Queue) MarkCompleted(ctx context.Context, job *Job, responseCode int) error {
	job.Status = StatusCompleted
	job.ResponseCode = responseCode
	job.Error = ""
	job.Body = nil
	job.Headers = nil
	job.UpdatedAt = time.Now()

	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}
	return q.client.Set(ctx, jobPrefix+job.ID, data, q.completedTTL).Err()
}

// SendToDeadLetter moves a job to the dead-letter queue. Body is preserved for debugging,
// under the failed_ttl (longer) so operators have time to investigate.
func (q *Queue) SendToDeadLetter(ctx context.Context, job *Job) error {
	job.Status = StatusDead
	job.UpdatedAt = time.Now()
	if err := q.UpdateJob(ctx, job); err != nil {
		return err
	}
	return q.client.LPush(ctx, deadLetterKey, job.ID).Err()
}

// Requeue puts a job back on the pending queue for retry.
func (q *Queue) Requeue(ctx context.Context, job *Job) error {
	job.Status = StatusQueued
	if err := q.UpdateJob(ctx, job); err != nil {
		return err
	}
	return q.client.LPush(ctx, queueKey, job.ID).Err()
}

// Depth returns the number of jobs in the pending queue.
func (q *Queue) Depth(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, queueKey).Result()
}
