// Package storeinmem provides an in-process reference SubmissionStore.
//
// It is intended for unit tests and local examples ONLY. State is lost on
// process restart; there is no replication, no durability, no concurrent
// access across processes. Do not use this in production. Implement
// stripenav.SubmissionStore against your own database instead.
package storeinmem

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
)

// Compile-time check that *Store satisfies stripenav.SubmissionStore.
// If the interface gains or changes a method, this line fails to build
// before any caller sees a misleading "[]invalid type" diagnostic.
var _ stripenav.SubmissionStore = (*Store)(nil)

// Store is the reference SubmissionStore backed by an in-process map.
type Store struct {
	mu   sync.Mutex
	rows map[string]stripenav.Submission
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{rows: map[string]stripenav.Submission{}}
}

// Put inserts a new submission. Returns an error if eventID already exists.
func (s *Store) Put(ctx context.Context, sub stripenav.Submission) error {
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

// Get returns the submission for eventID, or stripenav.ErrNotFound.
func (s *Store) Get(ctx context.Context, eventID string) (stripenav.Submission, error) {
	if err := ctx.Err(); err != nil {
		return stripenav.Submission{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return stripenav.Submission{}, stripenav.ErrNotFound
	}
	return sub, nil
}

// UpdateStatus atomically reads, mutates, and writes the submission.
func (s *Store) UpdateStatus(ctx context.Context, eventID string, mut func(*stripenav.Submission) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.rows[eventID]
	if !ok {
		return stripenav.ErrNotFound
	}
	if err := mut(&sub); err != nil {
		return err
	}
	sub.UpdatedAt = time.Now().UTC()
	s.rows[eventID] = sub
	return nil
}

// FindByInvoiceNumber returns every submission recorded for the given
// NAV invoice number, ordered by CreatedAt.
func (s *Store) FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]stripenav.Submission, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stripenav.Submission, 0)
	for _, sub := range s.rows {
		if sub.InvoiceNumber == invoiceNumber {
			out = append(out, sub)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListPending returns non-terminal submissions whose NextAttemptAt <= before.
func (s *Store) ListPending(ctx context.Context, before time.Time, limit int) ([]stripenav.Submission, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("storeinmem: limit must be > 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stripenav.Submission, 0)
	for _, sub := range s.rows {
		if sub.IsTerminal() {
			continue
		}
		if sub.NextAttemptAt.After(before) {
			continue
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
