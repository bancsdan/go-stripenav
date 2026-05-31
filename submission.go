package stripenav

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SubmissionStatus is the position of a submission in the bridge's
// internal state machine.
type SubmissionStatus string

const (
	StatusPending    SubmissionStatus = "pending"
	StatusSubmitted  SubmissionStatus = "submitted"
	StatusProcessing SubmissionStatus = "processing"
	StatusAccepted   SubmissionStatus = "accepted"
	StatusRejected   SubmissionStatus = "rejected"
	StatusAborted    SubmissionStatus = "aborted"
)

// EventKind tags what kind of work the submission represents.
type EventKind string

const (
	KindInvoice    EventKind = "invoice"
	KindAnnulment  EventKind = "annulment"
	KindCreditNote EventKind = "credit_note"
)

// Submission is the persisted record per Stripe event we are reporting
// to NAV.
type Submission struct {
	// EventID is the Stripe event id. Acts as primary key in the store.
	EventID string
	// Kind is invoice / credit_note / annulment.
	Kind EventKind
	// InvoiceNumber is the NAV invoice number being submitted. For a
	// STORNO this is the suffixed name (e.g. "X-STORNO"), not the
	// original — the link back to the original is held in ParentEventID.
	InvoiceNumber string
	// Operation is the NAV operation code this submission carries:
	// "CREATE", "MODIFY", "STORNO", or "ANNUL". Set by the handler at
	// persist time; the worker reads it to know which NAV endpoint and
	// operation literal to submit on retry.
	Operation string
	// ParentEventID, when non-empty, names another submission that must
	// reach StatusAccepted before this one can be submitted. NAV will
	// reject a STORNO/MODIFY whose original CREATE has not yet been
	// FINISHED on their side; the worker enforces the ordering.
	ParentEventID string
	// Status is the current position in the state machine.
	Status SubmissionStatus
	// Attempts counts how many times we have tried to submit to NAV.
	Attempts int
	// LastError captures the most recent failure message for the record.
	LastError string
	// TransactionID is the NAV transactionId returned by a successful
	// manageInvoice/manageAnnulment submission.
	TransactionID string
	// NextAttemptAt is when the worker is allowed to try again.
	NextAttemptAt time.Time
	// IssuedAt is the invoice's NAV-reportable issue date. The 24-hour
	// reporting deadline is measured from this timestamp.
	IssuedAt time.Time
	// CreatedAt/UpdatedAt are bookkeeping.
	CreatedAt time.Time
	UpdatedAt time.Time
	// RawEvent stores the verified Stripe webhook payload so the worker
	// can re-derive the NAV InvoiceData without depending on Stripe API
	// availability.
	RawEvent []byte
}

// validTransitions encodes the legal next-states for each current state.
var validTransitions = map[SubmissionStatus]map[SubmissionStatus]bool{
	StatusPending: {
		StatusPending:    true, // retry stays in pending with bumped attempt
		StatusSubmitted:  true,
		StatusRejected:   true,
		StatusAborted:    true,
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
// transition is not allowed. Terminal statuses cannot transition further.
func (s *Submission) Transition(next SubmissionStatus) error {
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

// ErrNotFound is returned by SubmissionStore implementations when the
// requested event id is unknown.
var ErrNotFound = errors.New("stripenav: submission not found")

// SubmissionStore is the persistence interface the bridge depends on.
// Implementations MUST make UpdateStatus atomic: concurrent calls against
// the same event id must serialise so neither mutation is lost.
type SubmissionStore interface {
	// Put inserts a new submission. It MUST fail (any non-nil error) if
	// an entry with the same EventID already exists, so the handler can
	// detect duplicate webhook deliveries.
	Put(ctx context.Context, s Submission) error

	// Get returns the submission for eventID, or ErrNotFound.
	Get(ctx context.Context, eventID string) (Submission, error)

	// UpdateStatus atomically reads, mutates, and writes the submission
	// identified by eventID. The mut function MUST be the only place
	// where Status, Attempts, LastError, NextAttemptAt and TransactionID
	// are modified by the worker.
	UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error

	// ListPending returns up to limit submissions whose status is
	// non-terminal and whose NextAttemptAt <= before.
	ListPending(ctx context.Context, before time.Time, limit int) ([]Submission, error)

	// FindByInvoiceNumber returns every submission previously recorded
	// for the given NAV invoice number. Used by the handler to discover
	// the parent CREATE when persisting a STORNO/MODIFY child.
	// Implementations SHOULD return entries in CreatedAt order.
	FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
