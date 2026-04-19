package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const (
	queueKey      = "relayer:jobs:pending"
	deadLetterKey = "relayer:jobs:dead"
	jobPrefix     = "relayer:job:"
)

type Queue struct {
	client *redis.Client
	logger zerolog.Logger
}

func NewQueue(client *redis.Client, logger zerolog.Logger) *Queue {
	return &Queue{client: client, logger: logger}
}

// Enqueue creates a new job and pushes it to the pending queue.
func (q *Queue) Enqueue(ctx context.Context, target, method, path string, headers map[string]string, body []byte, maxRetries int) (*Job, error) {
	job := &Job{
		ID:         uuid.New().String(),
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
	pipe.Set(ctx, jobPrefix+job.ID, data, 24*time.Hour)
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

// UpdateJob persists the current job state.
func (q *Queue) UpdateJob(ctx context.Context, job *Job) error {
	job.UpdatedAt = time.Now()
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}
	return q.client.Set(ctx, jobPrefix+job.ID, data, 24*time.Hour).Err()
}

// SendToDeadLetter moves a job to the dead-letter queue.
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
