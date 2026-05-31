package stripenav

import (
	"math/rand"
	"testing"
	"time"
)

// TestBackoffGrowsInternal exercises the unexported backoffFor helper.
// Behavioural coverage of backoff growth through the full submission
// flow lives in worker_test.go (external package).
func TestBackoffGrowsInternal(t *testing.T) {
	w := &Worker{
		baseBackoff: time.Second,
		maxBackoff:  time.Minute,
		rng:         rand.New(rand.NewSource(1)),
	}
	prev := time.Duration(0)
	for attempt := 1; attempt <= 4; attempt++ {
		got := w.backoffFor(attempt)
		if attempt > 1 && got < prev/2 {
			t.Errorf("backoff(%d) = %s, did not grow from %s", attempt, got, prev)
		}
		prev = got
	}
	if w.backoffFor(20) > time.Minute*2 {
		t.Errorf("backoff(20) exceeded max+jitter cap")
	}
}
