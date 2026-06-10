package stripenav

import (
	"github.com/bancsdan/go-stripenav/internal/submission"
)

// SubmissionStatus is the position of a submission in the bridge's
// internal state machine.
type SubmissionStatus = submission.Status

// The submission state machine's states, re-exported for consumers.
const (
	StatusPending    = submission.StatusPending
	StatusSubmitted  = submission.StatusSubmitted
	StatusProcessing = submission.StatusProcessing
	StatusAccepted   = submission.StatusAccepted
	StatusRejected   = submission.StatusRejected
	StatusAborted    = submission.StatusAborted
)

// EventKind tags what kind of work the submission represents.
type EventKind = submission.Kind

// The submission kinds the bridge produces, re-exported for consumers.
const (
	KindInvoice    = submission.KindInvoice
	KindAnnulment  = submission.KindAnnulment
	KindCreditNote = submission.KindCreditNote
)

// Submission is the persisted record per Stripe event we are reporting
// to NAV. See submission.Submission for the field-level documentation.
type Submission = submission.Submission

// SubmissionStore is the persistence interface the bridge depends on.
// See submission.Store for the contract details.
type SubmissionStore = submission.Store

// ErrNotFound is returned by SubmissionStore implementations when the
// requested event id is unknown.
var ErrNotFound = submission.ErrNotFound

// ErrAlreadyExists is wrapped by SubmissionStore.Put when a submission
// with the same EventID is already present. The handler treats it as a
// benign duplicate delivery rather than a store failure.
var ErrAlreadyExists = submission.ErrAlreadyExists

// ErrClaimLost is returned by RenewClaim and ReleaseClaim when the
// caller's claim is no longer valid (lease expired, taken by another
// worker, or the row no longer exists). Callers should treat this as
// "stop processing this submission, another worker has it now."
var ErrClaimLost = submission.ErrClaimLost
