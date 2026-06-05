package storeinmem_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
	"github.com/bancsdan/go-stripenav/internal/storeinmem"
)

func TestStore_PutGetDuplicate(t *testing.T) {
	s := storeinmem.New()
	ctx := context.Background()
	sub := stripenav.Submission{EventID: "evt_1", Status: stripenav.StatusPending, CreatedAt: time.Now()}
	if err := s.Put(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, sub); err == nil {
		t.Fatalf("expected duplicate error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected duplicate error: %v", err)
	}
	got, err := s.Get(ctx, "evt_1")
	if err != nil || got.EventID != "evt_1" {
		t.Fatalf("Get: %v %+v", err, got)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, stripenav.ErrNotFound) {
		t.Fatalf("Get(missing) = %v want ErrNotFound", err)
	}
}

func TestStore_UpdateStatusAtomic(t *testing.T) {
	s := storeinmem.New()
	ctx := context.Background()
	if err := s.Put(ctx, stripenav.Submission{EventID: "evt_1", Status: stripenav.StatusPending}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.UpdateStatus(ctx, "evt_1", func(sub *stripenav.Submission) error {
				sub.Attempts++
				return nil
			})
		}()
	}
	wg.Wait()
	got, err := s.Get(ctx, "evt_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != N {
		t.Fatalf("Attempts = %d after %d concurrent increments (want %d)", got.Attempts, N, N)
	}
}

func TestStore_ClaimBatchFiltersAndOrders(t *testing.T) {
	s := storeinmem.New()
	ctx := context.Background()
	now := time.Now()
	rows := []stripenav.Submission{
		{EventID: "a", Status: stripenav.StatusPending, NextAttemptAt: now.Add(-time.Minute), CreatedAt: now.Add(-3 * time.Minute)},
		{EventID: "b", Status: stripenav.StatusPending, NextAttemptAt: now.Add(time.Hour), CreatedAt: now.Add(-2 * time.Minute)},
		{EventID: "c", Status: stripenav.StatusAccepted, NextAttemptAt: now, CreatedAt: now.Add(-time.Minute)},
		{EventID: "d", Status: stripenav.StatusSubmitted, NextAttemptAt: now.Add(-time.Second), CreatedAt: now},
	}
	for _, r := range rows {
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.ClaimBatch(ctx, "worker-1", 10, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("ClaimBatch returned %d rows, want 2", len(out))
	}
	if out[0].EventID != "a" || out[1].EventID != "d" {
		t.Fatalf("ClaimBatch order: got [%s %s], want [a d]", out[0].EventID, out[1].EventID)
	}
	for _, c := range out {
		if c.ClaimedBy != "worker-1" {
			t.Errorf("row %s has ClaimedBy=%q, want worker-1", c.EventID, c.ClaimedBy)
		}
		if c.ClaimedUntil.IsZero() {
			t.Errorf("row %s has zero ClaimedUntil", c.EventID)
		}
	}
}

func TestStore_ClaimBatchSkipsClaimedRows(t *testing.T) {
	s := storeinmem.New()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.Put(ctx, stripenav.Submission{
		EventID: "shared", Status: stripenav.StatusPending,
		NextAttemptAt: now.Add(-time.Second), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimBatch(ctx, "worker-A", 10, 60*time.Second)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %v %+v", err, first)
	}
	second, err := s.ClaimBatch(ctx, "worker-B", 10, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("worker-B should not see row claimed by worker-A; got %+v", second)
	}
}

func TestStore_RenewAndReleaseClaim(t *testing.T) {
	s := storeinmem.New()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.Put(ctx, stripenav.Submission{
		EventID: "evt", Status: stripenav.StatusPending,
		NextAttemptAt: now.Add(-time.Second), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, _ := s.ClaimBatch(ctx, "worker-A", 1, 60*time.Second)
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claimed))
	}
	originalUntil := claimed[0].ClaimedUntil

	if err := s.RenewClaim(ctx, "evt", "worker-A", 120*time.Second); err != nil {
		t.Fatalf("RenewClaim: %v", err)
	}
	got, _ := s.Get(ctx, "evt")
	if !got.ClaimedUntil.After(originalUntil) {
		t.Errorf("ClaimedUntil did not extend: was %v, now %v", originalUntil, got.ClaimedUntil)
	}

	if err := s.RenewClaim(ctx, "evt", "imposter", 60*time.Second); !errors.Is(err, stripenav.ErrClaimLost) {
		t.Errorf("RenewClaim by imposter: got %v, want ErrClaimLost", err)
	}

	if err := s.ReleaseClaim(ctx, "evt", "worker-A"); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	got, _ = s.Get(ctx, "evt")
	if got.ClaimedBy != "" {
		t.Errorf("ClaimedBy not cleared after release: %q", got.ClaimedBy)
	}
}
