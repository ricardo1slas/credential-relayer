package queue

import (
	"context"
	"errors"
	"time"

	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"github.com/ricardo1slas/credential-relayer/internal/relay"
	"github.com/rs/zerolog"
)

type WorkerPoolConfig struct {
	Workers    int
	RetryDelay time.Duration
	MaxRetries int
	// ReapAfter is how long a job may stay claimed before it is presumed abandoned (worker crash
	// or shutdown) and requeued. It must exceed the longest delivery plus RetryDelay.
	ReapAfter time.Duration
}

type WorkerPool struct {
	queue   *Queue
	handler *relay.Handler
	cfg     WorkerPoolConfig
	logger  zerolog.Logger
	metrics Recorder
}

func NewWorkerPool(q *Queue, handler *relay.Handler, cfg WorkerPoolConfig, logger zerolog.Logger) *WorkerPool {
	if cfg.Workers == 0 {
		cfg.Workers = 5
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 30 * time.Second
	}
	if cfg.ReapAfter == 0 {
		cfg.ReapAfter = cfg.RetryDelay + 5*time.Minute
	}
	return &WorkerPool{
		queue:   q,
		handler: handler,
		cfg:     cfg,
		logger:  logger,
		metrics: nopRecorder{},
	}
}

// SetMetrics attaches a metrics recorder. Passing nil keeps the no-op default.
func (wp *WorkerPool) SetMetrics(r Recorder) {
	if r != nil {
		wp.metrics = r
	}
}

// Start launches worker goroutines. Blocks until ctx is cancelled.
func (wp *WorkerPool) Start(ctx context.Context) {
	wp.logger.Info().Int("workers", wp.cfg.Workers).Msg("starting worker pool")

	for i := 0; i < wp.cfg.Workers; i++ {
		go wp.worker(ctx, i)
	}
	go wp.reaper(ctx)

	<-ctx.Done()
	wp.logger.Info().Msg("worker pool shutting down")
}

func (wp *WorkerPool) worker(ctx context.Context, id int) {
	logger := wp.logger.With().Int("worker_id", id).Logger()
	logger.Info().Msg("worker started")

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("worker stopped")
			return
		default:
		}

		job, err := wp.queue.Dequeue(ctx, 5*time.Second)
		if err != nil {
			// BLMove timeout or context cancelled -- just loop.
			continue
		}

		wp.processJob(ctx, job, logger)
	}
}

// reaper requeues jobs left claimed by a crashed or stopped worker, from any instance.
func (wp *WorkerPool) reaper(ctx context.Context) {
	tick := time.NewTicker(wp.cfg.ReapAfter / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n, err := wp.queue.ReapStale(ctx, wp.cfg.ReapAfter)
		if err != nil {
			wp.logger.Warn().Err(err).Msg("reaping abandoned jobs")
		}
		if n > 0 {
			wp.logger.Warn().Int("jobs", n).Msg("requeued jobs abandoned by a worker")
		}
	}
}

func (wp *WorkerPool) processJob(ctx context.Context, job *Job, logger zerolog.Logger) {
	logger = logger.With().Str("job_id", job.ID).Str("target", job.Target).Logger()
	logger.Info().Msg("processing job")

	job.Status = StatusProcessing
	job.Attempts++
	_ = wp.queue.UpdateJob(ctx, job)

	// Use the relay handler's internal method to execute the relay.
	// Attribute the call to the key that queued it (metrics, logs), scoped to this one target:
	// the key's scope was already checked at enqueue.
	jobCtx := auth.WithKey(ctx, job.Owner, []string{job.Target})
	resp, _, err := wp.handler.ExecuteRelay(jobCtx, job.Target, job.Method, job.Path, job.Headers, job.Body)
	if err != nil {
		logger.Error().Err(err).Int("attempt", job.Attempts).Msg("job relay failed")

		job.Error = err.Error()
		// A policy rejection (e.g. config tightened after enqueue) never succeeds on retry.
		var rerr *relay.RequestError
		if job.Attempts >= job.MaxRetries || errors.As(err, &rerr) {
			logger.Warn().Msg("sending to dead-letter queue")
			_ = wp.queue.SendToDeadLetter(ctx, job)
			wp.metrics.RecordJob("dead")
			return
		}

		// Wait before requeue.
		select {
		case <-time.After(wp.cfg.RetryDelay):
		case <-ctx.Done():
			return
		}

		_ = wp.queue.Requeue(ctx, job)
		return
	}

	_ = wp.queue.MarkCompleted(ctx, job, resp.StatusCode)
	_ = resp.Body.Close()
	wp.metrics.RecordJob("completed")

	logger.Info().Int("status", resp.StatusCode).Msg("job completed")
}
