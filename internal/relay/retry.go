package relay

import (
	"math"
	"math/rand/v2"
	"time"
)

const (
	baseDelay = 500 * time.Millisecond
	maxDelay  = 30 * time.Second
)

// backoff calculates exponential backoff with jitter.
// attempt is 0-indexed: attempt 0 -> ~500ms, attempt 1 -> ~1s, attempt 2 -> ~2s, etc.
func backoff(attempt int) time.Duration {
	delay := float64(baseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}
	// Add jitter: random value between 50% and 100% of the delay.
	// #nosec G404 -- jitter is timing variance, not a security boundary; CSPRNG overhead unwarranted.
	jitter := 0.5 + rand.Float64()*0.5
	return time.Duration(delay * jitter)
}
