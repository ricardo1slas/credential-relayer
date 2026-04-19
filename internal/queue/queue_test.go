package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15}) // use DB 15 for tests
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available at %s: %v", addr, err)
	}

	// Clean test DB before each test.
	client.FlushDB(ctx)

	t.Cleanup(func() {
		client.FlushDB(context.Background())
		_ = client.Close()
	})

	return client
}

func TestEnqueueDequeue(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)

	ctx := context.Background()

	// Enqueue a job.
	job, err := q.Enqueue(ctx, "test-target", "POST", "/webhook", nil, []byte(`{"data":"hello"}`), 3)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if job.ID == "" {
		t.Fatal("expected non-empty job ID")
	}
	if job.Status != StatusQueued {
		t.Errorf("expected status 'queued', got %q", job.Status)
	}

	// Check queue depth.
	depth, err := q.Depth(ctx)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 1 {
		t.Errorf("expected depth 1, got %d", depth)
	}

	// Dequeue the job.
	dequeued, err := q.Dequeue(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}

	if dequeued.ID != job.ID {
		t.Errorf("expected job ID %q, got %q", job.ID, dequeued.ID)
	}
	if dequeued.Target != "test-target" {
		t.Errorf("expected target 'test-target', got %q", dequeued.Target)
	}

	// Queue should be empty now.
	depth, err = q.Depth(ctx)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 0 {
		t.Errorf("expected depth 0 after dequeue, got %d", depth)
	}
}

func TestGetJob(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)
	ctx := context.Background()

	job, err := q.Enqueue(ctx, "svc", "GET", "/", nil, nil, 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	fetched, err := q.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if fetched.ID != job.ID {
		t.Errorf("expected %q, got %q", job.ID, fetched.ID)
	}
}

func TestGetJobNotFound(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)
	ctx := context.Background()

	_, err := q.GetJob(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
}

func TestUpdateJob(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)
	ctx := context.Background()

	job, _ := q.Enqueue(ctx, "svc", "POST", "/", nil, nil, 3)

	job.Status = StatusProcessing
	job.Attempts = 1
	if err := q.UpdateJob(ctx, job); err != nil {
		t.Fatalf("update: %v", err)
	}

	fetched, _ := q.GetJob(ctx, job.ID)
	if fetched.Status != StatusProcessing {
		t.Errorf("expected 'processing', got %q", fetched.Status)
	}
	if fetched.Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", fetched.Attempts)
	}
}

func TestSendToDeadLetter(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)
	ctx := context.Background()

	job, _ := q.Enqueue(ctx, "svc", "POST", "/", nil, nil, 1)

	if err := q.SendToDeadLetter(ctx, job); err != nil {
		t.Fatalf("dead letter: %v", err)
	}

	fetched, _ := q.GetJob(ctx, job.ID)
	if fetched.Status != StatusDead {
		t.Errorf("expected 'dead', got %q", fetched.Status)
	}

	// Check dead-letter queue has the job.
	dlLen := client.LLen(ctx, deadLetterKey).Val()
	if dlLen != 1 {
		t.Errorf("expected dead-letter length 1, got %d", dlLen)
	}
}

func TestRequeue(t *testing.T) {
	client := testRedisClient(t)
	logger := zerolog.Nop()
	q := NewQueue(client, Config{}, logger)
	ctx := context.Background()

	job, _ := q.Enqueue(ctx, "svc", "POST", "/", nil, nil, 3)

	// Dequeue it.
	_, _ = q.Dequeue(ctx, 2*time.Second)

	// Requeue it.
	job.Status = StatusFailed
	if err := q.Requeue(ctx, job); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	// Should be back in the queue.
	depth, _ := q.Depth(ctx)
	if depth != 1 {
		t.Errorf("expected depth 1 after requeue, got %d", depth)
	}

	fetched, _ := q.GetJob(ctx, job.ID)
	if fetched.Status != StatusQueued {
		t.Errorf("expected 'queued' after requeue, got %q", fetched.Status)
	}
}
