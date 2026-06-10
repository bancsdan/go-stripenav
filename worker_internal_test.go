package stripenav

import (
	"math/rand"
	"testing"
	"time"

	"github.com/bancsdan/go-stripenav/nav/schemas"
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
	if w.backoffFor(20) > time.Minute {
		t.Errorf("backoff(20) exceeded MaxBackoff (jitter must apply before the cap)")
	}
}

// TestOverallStatusInternal pins the batch-collapse rules: ABORTED
// dominates, FINISHED requires every operation final, and a partially
// finished batch must NOT report final.
func TestOverallStatusInternal(t *testing.T) {
	mk := func(statuses ...string) schemas.QueryTransactionStatusResponse {
		prs := make([]schemas.ProcessingResult, len(statuses))
		for i, s := range statuses {
			prs[i] = schemas.ProcessingResult{Index: i + 1, InvoiceStatus: s}
		}
		return schemas.QueryTransactionStatusResponse{
			ProcessingResults: schemas.ProcessingResults{ProcessingResult: prs},
		}
	}
	cases := []struct {
		name string
		resp schemas.QueryTransactionStatusResponse
		want string
	}{
		{"empty", mk(), ""},
		{"all finished", mk("FINISHED", "DONE"), "FINISHED"},
		{"any aborted wins", mk("FINISHED", "ABORTED"), "ABORTED"},
		{"first finished but second still processing", mk("FINISHED", "PROCESSING"), "PROCESSING"},
		{"received only", mk("RECEIVED"), "RECEIVED"},
		{"non-final empty status keeps polling", mk("FINISHED", ""), ""},
	}
	for _, c := range cases {
		if got := overallStatus(c.resp); got != c.want {
			t.Errorf("%s: overallStatus = %q, want %q", c.name, got, c.want)
		}
	}
}
