// Package submission holds the persistence types and interface the
// bridge uses for its state machine. It exists as a separate package so
// the storeinmem reference implementation can import it without
// creating an import cycle with the root stripenav package, which
// itself defaults Config.Store to an in-memory store. Consumers reach
// these types through type aliases re-exported from package stripenav;
// nothing in this package is part of the documented public API.
package submission

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Status is the position of a submission in the bridge's internal
// state machine.
type Status string

const (
	StatusPending    Status = "pending"
	StatusSubmitted  Status = "submitted"
	StatusProcessing Status = "processing"
	StatusAccepted   Status = "accepted"
	StatusRejected   Status = "rejected"
	StatusAborted    Status = "aborted"
)

// Kind tags what kind of work the submission represents.
type Kind string

const (
	KindInvoice    Kind = "invoice"
	KindAnnulment  Kind = "annulment"
	KindCreditNote Kind = "credit_note"
)

// Submission is the persisted record per Stripe event we are reporting
// to NAV.
type Submission struct {
	EventID       string
	Kind          Kind
	InvoiceNumber string
	Operation     string
	ParentEventID string
	Status        Status
	Attempts      int
	LastError     string
	TransactionID string
	NextAttemptAt time.Time
	IssuedAt      time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RawEvent      []byte
	ClaimedBy     string
	ClaimedUntil  time.Time
}

var validTransitions = map[Status]map[Status]bool{
	StatusPending: {
		StatusPending:   true,
		StatusSubmitted: true,
		StatusRejected:  true,
		StatusAborted:   true,
	},
	StatusSubmitted: {
		StatusProcessing: true,
		StatusAccepted:   true,
		StatusRejected:   true,
		StatusAborted:    true,
	},
	StatusProcessing: {
		StatusProcessing: true,
		StatusAccepted:   true,
		StatusRejected:   true,
		StatusAborted:    true,
	},
}

// Transition advances the submission to next, or returns an error if the
// transition is not allowed.
func (s *Submission) Transition(next Status) error {
	if s.Status == next && (next == StatusPending || next == StatusProcessing) {
		s.Status = next
		return nil
	}
	allowed, ok := validTransitions[s.Status]
	if !ok {
		return fmt.Errorf("submission: terminal status %q cannot transition", s.Status)
	}
	if !allowed[next] {
		return fmt.Errorf("submission: cannot transition %q → %q", s.Status, next)
	}
	s.Status = next
	return nil
}

// IsTerminal reports whether s is in an end state.
func (s *Submission) IsTerminal() bool {
	switch s.Status {
	case StatusAccepted, StatusRejected, StatusAborted:
		return true
	}
	return false
}

// ErrNotFound is returned by Store implementations when the requested
// event id is unknown.
var ErrNotFound = errors.New("stripenav: submission not found")

// ErrClaimLost is returned by RenewClaim and ReleaseClaim when the
// caller's claim is no longer valid.
var ErrClaimLost = errors.New("stripenav: claim lost")

// Store is the persistence interface the bridge depends on.
//
// The store doubles as a distributed work queue: ClaimBatch reserves
// non-terminal submissions for a single claimer at a time, with a TTL
// lease so a crashed claimer's work eventually becomes available to
// others. Implementations MUST ensure ClaimBatch is atomic across
// concurrent callers — two ClaimBatch invocations with different
// claimer ids MUST NOT both return the same row.
//
// Beyond ClaimBatch, UpdateStatus MUST be atomic per-row: concurrent
// mutators of the same row serialise.
type Store interface {
	// Put inserts a new submission. It MUST fail (any non-nil error) if
	// an entry with the same EventID already exists, so the handler can
	// detect duplicate webhook deliveries.
	Put(ctx context.Context, s Submission) error

	// Get returns the submission for eventID, or ErrNotFound.
	Get(ctx context.Context, eventID string) (Submission, error)

	// UpdateStatus atomically reads, mutates, and writes the submission
	// identified by eventID. The mut function MUST be the only place
	// where Status, Attempts, LastError, NextAttemptAt, TransactionID,
	// or RawEvent are modified by the worker.
	UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error

	// ClaimBatch reserves up to limit non-terminal submissions whose
	// NextAttemptAt <= now and whose existing claim has expired (if
	// any). The claimer holds each returned row for lease, after which
	// it becomes claimable by another worker.
	//
	// Implementations MUST return claimed Submissions with their
	// ClaimedBy and ClaimedUntil fields populated to reflect the
	// just-applied claim.
	//
	// Implementations MUST NOT return rows that are currently held under
	// a valid lease — even when the existing holder matches the caller's
	// claimer id. Allowing a worker to re-claim its own active rows would
	// spawn duplicate lifecycle goroutines for the same submission; the
	// first lifecycle's ReleaseClaim would then race the second's
	// RenewClaim and trip a spurious "claim lost" error. A Postgres
	// adapter built on SELECT … FOR UPDATE SKIP LOCKED is naturally
	// immune; an in-process implementation must guard explicitly.
	ClaimBatch(ctx context.Context, claimer string, limit int, lease time.Duration) ([]Submission, error)

	// RenewClaim extends the lease on a previously-claimed row. Returns
	// ErrClaimLost if claimer is no longer the holder.
	RenewClaim(ctx context.Context, eventID, claimer string, lease time.Duration) error

	// ReleaseClaim clears claimer's hold on the row. Returns ErrClaimLost
	// if claimer is no longer the holder. ReleaseClaim does NOT change
	// the submission's status or NextAttemptAt — callers UpdateStatus
	// first, then release.
	ReleaseClaim(ctx context.Context, eventID, claimer string) error

	// FindByInvoiceNumber returns every submission previously recorded
	// for the given NAV invoice number. Used by the handler to discover
	// the parent CREATE when persisting a STORNO/MODIFY child.
	// Implementations SHOULD return entries in CreatedAt order.
	FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
