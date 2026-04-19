package relay

import (
	"testing"
	"time"
)

func TestBackoffExponentialGrowth(t *testing.T) {
	// Run multiple samples to account for jitter.
	for attempt := 0; attempt < 5; attempt++ {
		d := backoff(attempt)
		minExpected := time.Duration(float64(baseDelay) * float64(int(1)<<attempt) * 0.5)
		maxExpected := time.Duration(float64(baseDelay) * float64(int(1)<<attempt))

		if d < minExpected || d > maxExpected {
			t.Errorf("attempt %d: backoff %v not in range [%v, %v]", attempt, d, minExpected, maxExpected)
		}
	}
}

func TestBackoffCap(t *testing.T) {
	// Very high attempt should not exceed maxDelay.
	d := backoff(100)
	if d > maxDelay {
		t.Errorf("backoff %v exceeds max %v", d, maxDelay)
	}
}
