// Package storeinmem provides an in-process reference SubmissionStore.
// Used as the bridge's default when no Store is supplied — appropriate
// for unit tests and local examples only. State is lost on process
// restart.
package storeinmem

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bancsdan/go-stripenav/internal/submission"
)

// Compile-time interface check.
var _ submission.Store = (*Store)(nil)

// Store is the reference Store backed by an in-process map.
type Store struct {
	mu   sync.Mutex
	rows map[string]submission.Submission
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{rows: map[string]submission.Submission{}}
}

// Put inserts a new submission. Returns an error if eventID already exists.
func (s *Store) Put(ctx context.Context, sub submission.Submission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[sub.EventID]; exists {
		return fmt.Errorf("storeinmem: submission %q already exists", sub.EventID)
	}
	s.rows[sub.EventID] = sub
	return nil
}

// Get returns the submission for eventID, or submission.ErrNotFound.
func (s *Store) Get(ctx context.Context, eventID string) (submission.Submission, error) {
	if err := ctx.Err(); err != nil {
		return submission.Submission{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return submission.Submission{}, submission.ErrNotFound
	}
	return sub, nil
}

// UpdateStatus atomically reads, mutates, and writes the submission.
func (s *Store) UpdateStatus(ctx context.Context, eventID string, mut func(*submission.Submission) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return submission.ErrNotFound
	}
	if err := mut(&sub); err != nil {
		return err
	}
	sub.UpdatedAt = time.Now().UTC()
	s.rows[eventID] = sub
	return nil
}

// ClaimBatch reserves up to limit non-terminal rows that are due
// (NextAttemptAt <= now) and either unclaimed or whose lease has
// expired. Mirrors the FOR UPDATE SKIP LOCKED semantics the Postgres
// adapter implements, but within a single process.
func (s *Store) ClaimBatch(ctx context.Context, claimer string, limit int, lease time.Duration) ([]submission.Submission, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("storeinmem: limit must be > 0")
	}
	if claimer == "" {
		return nil, fmt.Errorf("storeinmem: claimer is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	candidates := make([]submission.Submission, 0)
	for _, sub := range s.rows {
		if sub.IsTerminal() {
			continue
		}
		if sub.NextAttemptAt.After(now) {
			continue
		}
		// Skip rows already held with a valid lease — regardless of
		// whether the holder is another worker or ourselves. Allowing
		// the same claimer to re-claim its own active rows would
		// spawn a duplicate lifecycle goroutine for the row, and the
		// first lifecycle's ReleaseClaim would then trip the second
		// lifecycle's renew loop into a spurious "claim lost"
		// warning. (The Postgres SKIP LOCKED equivalent doesn't allow
		// this because the row lock blocks the duplicate selection.)
		if sub.ClaimedBy != "" && sub.ClaimedUntil.After(now) {
			continue
		}
		candidates = append(candidates, sub)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	out := make([]submission.Submission, 0, len(candidates))
	until := now.Add(lease)
	for _, sub := range candidates {
		sub.ClaimedBy = claimer
		sub.ClaimedUntil = until
		sub.UpdatedAt = now
		s.rows[sub.EventID] = sub
		out = append(out, sub)
	}
	return out, nil
}

// RenewClaim extends the lease on a row held by claimer.
func (s *Store) RenewClaim(ctx context.Context, eventID, claimer string, lease time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if claimer == "" {
		return fmt.Errorf("storeinmem: claimer is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return submission.ErrNotFound
	}
	// Lease must still be ours (either unexpired or expired but unclaimed by others).
	if sub.ClaimedBy != claimer {
		return submission.ErrClaimLost
	}
	if sub.ClaimedUntil.Before(now) {
		// Lease has technically expired but no other claimer has taken it.
		// We allow the renewal; this models the SQL "WHERE claimed_by=$1
		// AND claimed_until > now()" check loosened to just claimed_by,
		// since a single-process store has no other claimers.
	}
	sub.ClaimedUntil = now.Add(lease)
	sub.UpdatedAt = now
	s.rows[eventID] = sub
	return nil
}

// ReleaseClaim clears claimer's hold.
func (s *Store) ReleaseClaim(ctx context.Context, eventID, claimer string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return submission.ErrNotFound
	}
	if sub.ClaimedBy != claimer {
		return submission.ErrClaimLost
	}
	sub.ClaimedBy = ""
	sub.ClaimedUntil = time.Time{}
	sub.UpdatedAt = time.Now().UTC()
	s.rows[eventID] = sub
	return nil
}

// FindByInvoiceNumber returns every submission recorded for the given
// NAV invoice number, ordered by CreatedAt.
func (s *Store) FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]submission.Submission, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]submission.Submission, 0)
	for _, sub := range s.rows {
		if sub.InvoiceNumber == invoiceNumber {
			out = append(out, sub)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
