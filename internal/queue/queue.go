package queue

import (
	"context"
	"errors"
	"fmt"
	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const (
	queueKey      = "relayer:jobs:pending"
	processingKey = "relayer:jobs:processing" // claimed by a worker, not yet finished
	claimsKey     = "relayer:jobs:claims"     // zset: job ID -> unix time it was first seen claimed
	deadLetterKey = "relayer:jobs:dead"
	jobPrefix     = "relayer:job:"

	defaultCompletedTTL = time.Hour
	defaultFailedTTL    = 24 * time.Hour
)

// ErrJobNotFound means the job record is gone (never existed, or its TTL expired).
var ErrJobNotFound = errors.New("job not found")

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

// Dequeue blocks until a job is available or the context is cancelled. The ID moves atomically
// into the processing list, so a worker that dies mid-job leaves it for ReapStale instead of
// losing it. Every dequeued job must end in MarkCompleted, Requeue or SendToDeadLetter.
func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) (*Job, error) {
	id, err := q.client.BLMove(ctx, queueKey, processingKey, "RIGHT", "LEFT", timeout).Result()
	if err != nil {
		return nil, err
	}
	q.client.ZAddNX(ctx, claimsKey, redis.Z{Score: float64(time.Now().Unix()), Member: id})

	job, err := q.GetJob(ctx, id)
	if errors.Is(err, ErrJobNotFound) {
		// Nothing left to deliver; drop the claim so the reaper doesn't requeue it forever.
		_, _ = q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.LRem(ctx, processingKey, 1, id)
			p.ZRem(ctx, claimsKey, id)
			return nil
		})
	}
	return job, err
}

// reapScript returns a claimed ID to the pending queue only if it is still claimed, so two
// instances reaping at once can't push it twice.
var reapScript = redis.NewScript(`
if redis.call('LREM', KEYS[1], 1, ARGV[1]) == 1 then
  redis.call('ZREM', KEYS[2], ARGV[1])
  redis.call('LPUSH', KEYS[3], ARGV[1])
  return 1
end
return 0`)

// ReapStale requeues jobs claimed more than olderThan ago: their worker crashed or shut down
// mid-job. This makes delivery at-least-once: olderThan must exceed the longest a live worker can
// hold a job, or a slow delivery races its own requeued copy.
func (q *Queue) ReapStale(ctx context.Context, olderThan time.Duration) (int, error) {
	ids, err := q.client.LRange(ctx, processingKey, 0, -1).Result()
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, id := range ids {
		claimed, err := q.client.ZScore(ctx, claimsKey, id).Result()
		if err == redis.Nil {
			// Claimed between BLMOVE and ZADD by a worker that died, or that is about to ZADD
			// itself (NX keeps whichever lands first; both are "now"). Start its clock.
			q.client.ZAddNX(ctx, claimsKey, redis.Z{Score: float64(time.Now().Unix()), Member: id})
			continue
		}
		if err != nil {
			return reaped, err
		}
		if time.Since(time.Unix(int64(claimed), 0)) < olderThan {
			continue
		}
		n, err := reapScript.Run(ctx, q.client, []string{processingKey, claimsKey, queueKey}, id).Int()
		if err != nil {
			return reaped, err
		}
		reaped += n
	}
	return reaped, nil
}

// GetJob retrieves a job by ID.
func (q *Queue) GetJob(ctx context.Context, id string) (*Job, error) {
	data, err := q.client.Get(ctx, jobPrefix+id).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("job %s: %w", id, ErrJobNotFound)
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
	return q.finish(ctx, job, q.completedTTL, "")
}

// SendToDeadLetter moves a job to the dead-letter queue. Body is preserved for debugging,
// under the failed_ttl (longer) so operators have time to investigate.
func (q *Queue) SendToDeadLetter(ctx context.Context, job *Job) error {
	job.Status = StatusDead
	return q.finish(ctx, job, q.failedTTL, deadLetterKey)
}

// Requeue puts a job back on the pending queue for retry.
func (q *Queue) Requeue(ctx context.Context, job *Job) error {
	job.Status = StatusQueued
	return q.finish(ctx, job, q.failedTTL, queueKey)
}

// finish saves job, releases its processing claim and optionally pushes it onto another list,
// all in one transaction so a crash can't leave it both claimed and queued.
func (q *Queue) finish(ctx context.Context, job *Job, ttl time.Duration, pushTo string) error {
	job.UpdatedAt = time.Now()
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}
	_, err = q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, jobPrefix+job.ID, data, ttl)
		p.LRem(ctx, processingKey, 1, job.ID)
		p.ZRem(ctx, claimsKey, job.ID)
		if pushTo != "" {
			p.LPush(ctx, pushTo, job.ID)
		}
		return nil
	})
	return err
}

// Depth returns the number of jobs in the pending queue.
func (q *Queue) Depth(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, queueKey).Result()
}
