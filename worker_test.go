package stripenav_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
	"github.com/bancsdan/go-stripenav/storeinmem"
)

// fakeNAVClient is a configurable NAVClient for unit tests.
type fakeNAVClient struct {
	submitFn func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error)
	annulFn  func(ctx context.Context, ops []nav.AnnulmentOperation) (nav.SubmitResult, error)
	statusFn func(ctx context.Context, tx string, returnOriginal bool) (schemas.QueryTransactionStatusResponse, error)

	submitCalls int32
	statusCalls int32
}

func (f *fakeNAVClient) SubmitInvoice(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
	atomic.AddInt32(&f.submitCalls, 1)
	return f.submitFn(ctx, ops)
}

func (f *fakeNAVClient) AnnulInvoice(ctx context.Context, ops []nav.AnnulmentOperation) (nav.SubmitResult, error) {
	return f.annulFn(ctx, ops)
}

func (f *fakeNAVClient) QueryTransactionStatus(ctx context.Context, tx string, returnOriginal bool) (schemas.QueryTransactionStatusResponse, error) {
	atomic.AddInt32(&f.statusCalls, 1)
	if f.statusFn == nil {
		// Default: report FINISHED so lifecycle drives to accepted.
		return schemas.QueryTransactionStatusResponse{
			ProcessingResults: schemas.ProcessingResults{
				ProcessingResult: []schemas.ProcessingResult{{Index: 1, InvoiceStatus: "FINISHED"}},
			},
		}, nil
	}
	return f.statusFn(ctx, tx, returnOriginal)
}

func sampleInvoiceData() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><InvoiceData xmlns="http://schemas.nav.gov.hu/OSA/3.0/data"><invoiceNumber>X</invoiceNumber></InvoiceData>`)
}

func newTestWorker(t *testing.T, client stripenav.NAVClient, clock func() time.Time) (*stripenav.Worker, *storeinmem.Store) {
	t.Helper()
	store := storeinmem.New()
	w, err := stripenav.NewWorker(stripenav.WorkerConfig{
		Store:        store,
		Client:       client,
		Clock:        clock,
		ClaimerID:    "test",
		MaxSleep:     time.Second,
		PollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w, store
}

// TestWorker_HappyPath: lifecycle goroutine drives a row from pending
// through submit, poll (PROCESSING), poll (FINISHED), to accepted —
// all inside one Tick because each step's wait is ≤ pollInterval.
func TestWorker_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	pollCount := int32(0)
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{TransactionID: "T1"}, nil
		},
		statusFn: func(ctx context.Context, tx string, _ bool) (schemas.QueryTransactionStatusResponse, error) {
			n := atomic.AddInt32(&pollCount, 1)
			st := "PROCESSING"
			if n > 1 {
				st = "FINISHED"
			}
			return schemas.QueryTransactionStatusResponse{
				ProcessingResults: schemas.ProcessingResults{
					ProcessingResult: []schemas.ProcessingResult{{Index: 1, InvoiceStatus: st}},
				},
			}, nil
		},
	}
	w, store := newTestWorker(t, client, time.Now) // use real clock so sleeps elapse

	sub := stripenav.Submission{
		EventID:       "evt_happy",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now.Add(-time.Second),
		RawEvent:      sampleInvoiceData(),
	}
	if err := store.Put(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	got, _ := store.Get(context.Background(), "evt_happy")
	if got.Status != stripenav.StatusAccepted {
		t.Fatalf("final status = %s (want accepted); attempts=%d txid=%s last=%q",
			got.Status, got.Attempts, got.TransactionID, got.LastError)
	}
	if got.TransactionID != "T1" {
		t.Errorf("TransactionID = %q, want T1", got.TransactionID)
	}
}

// TestWorker_24HourDeadline: row issued >24h ago is aborted immediately
// without any NAV call.
func TestWorker_24HourDeadline(t *testing.T) {
	issued := time.Date(2026, 5, 26, 8, 0, 0, 0, time.UTC)
	clockNow := issued.Add(25 * time.Hour)
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{}, errors.New("must not be called")
		},
	}
	w, store := newTestWorker(t, client, func() time.Time { return clockNow })

	if err := store.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_late",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      issued,
		CreatedAt:     issued,
		NextAttemptAt: time.Now().Add(-time.Second), // due now in real time
		RawEvent:      sampleInvoiceData(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "evt_late")
	if got.Status != stripenav.StatusAborted {
		t.Fatalf("expected aborted, got %s", got.Status)
	}
	if !strings.Contains(got.LastError, "24-hour") {
		t.Fatalf("expected deadline error, got %q", got.LastError)
	}
	if atomic.LoadInt32(&client.submitCalls) != 0 {
		t.Fatalf("expected zero NAV calls, got %d", client.submitCalls)
	}
}

// TestWorker_TransientFailureRetries: first attempt fails with retriable
// error. The lifecycle goroutine exits (because the backoff is well over
// pollInterval). A second Tick after the backoff has elapsed picks up
// the row and submits successfully.
func TestWorker_TransientFailureRetries(t *testing.T) {
	var calls int32
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nav.SubmitResult{}, &nav.NAVError{HTTPStatus: 500, Code: "INTERNAL_ERROR", Retriable: true, Message: "boom"}
			}
			return nav.SubmitResult{TransactionID: "T2"}, nil
		},
		statusFn: func(ctx context.Context, tx string, _ bool) (schemas.QueryTransactionStatusResponse, error) {
			return schemas.QueryTransactionStatusResponse{
				ProcessingResults: schemas.ProcessingResults{
					ProcessingResult: []schemas.ProcessingResult{{Index: 1, InvoiceStatus: "FINISHED"}},
				},
			}, nil
		},
	}
	now := time.Now().UTC()
	w, store := newTestWorker(t, client, time.Now)
	if err := store.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_retry",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now.Add(-time.Second),
		RawEvent:      sampleInvoiceData(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "evt_retry")
	if got.Status != stripenav.StatusPending || got.Attempts != 1 {
		t.Fatalf("after failure: status=%s attempts=%d", got.Status, got.Attempts)
	}
	if !got.NextAttemptAt.After(time.Now()) {
		t.Fatalf("NextAttemptAt should be in the future, got %s", got.NextAttemptAt)
	}

	// Pull NextAttemptAt back to "now" so ClaimBatch picks the row up,
	// then drive the retry.
	_ = store.UpdateStatus(context.Background(), "evt_retry", func(s *stripenav.Submission) error {
		s.NextAttemptAt = time.Now().Add(-time.Second)
		return nil
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(context.Background(), "evt_retry")
	// After retry succeeds, the lifecycle goroutine drives through poll
	// (FINISHED) to accepted, all within the same Tick.
	if got.Status != stripenav.StatusAccepted || got.TransactionID != "T2" {
		t.Fatalf("after retry: status=%s txid=%s", got.Status, got.TransactionID)
	}
}

// flakyRenewStore wraps an in-memory store and lets RenewClaim be
// forced to fail to simulate the lease being stolen by another
// replica or the store dropping the claim.
type flakyRenewStore struct {
	*storeinmem.Store
	renewErr atomic.Value // error or nil
}

func (s *flakyRenewStore) RenewClaim(ctx context.Context, eventID, claimer string, lease time.Duration) error {
	if v, ok := s.renewErr.Load().(error); ok && v != nil {
		return v
	}
	return s.Store.RenewClaim(ctx, eventID, claimer, lease)
}

func (s *flakyRenewStore) setRenewErr(err error) {
	s.renewErr.Store(err)
}

// TestWorker_LostLeaseStopsLifecycle pins the multi-replica safety
// fix: if the lease can't be renewed (claim stolen, store dropped
// the row, network split), the lifecycle goroutine must stop touching
// the row immediately rather than continuing to call NAV and
// UpdateStatus under a stale claim. Two replicas both processing the
// same row would otherwise submit the same invoice to NAV twice.
func TestWorker_LostLeaseStopsLifecycle(t *testing.T) {
	base := storeinmem.New()
	flaky := &flakyRenewStore{Store: base}

	submitStarted := make(chan struct{})
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			close(submitStarted)
			// Block until the lifecycle's leaseCtx cancels us.
			<-ctx.Done()
			return nav.SubmitResult{}, ctx.Err()
		},
	}

	// Short lease so the renew loop ticks well within the test window.
	w, err := stripenav.NewWorker(stripenav.WorkerConfig{
		Store:         flaky,
		Client:        client,
		Clock:         time.Now,
		ClaimerID:     "test",
		MaxSleep:      time.Second,
		PollInterval:  100 * time.Millisecond,
		LeaseDuration: 60 * time.Millisecond, // renew every ~20ms
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	now := time.Now().UTC()
	if err := base.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_lease_lost",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now.Add(-time.Second),
		RawEvent:      sampleInvoiceData(),
	}); err != nil {
		t.Fatal(err)
	}

	// Once the submit blocks, simulate the claim being stolen by
	// flipping RenewClaim to fail.
	go func() {
		<-submitStarted
		flaky.setRenewErr(stripenav.ErrClaimLost)
	}()

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Submit was called exactly once, blocked on context, then aborted
	// by the lease-cancelled leaseCtx. The row must still be Pending —
	// the lifecycle should NOT have transitioned it to Submitted under
	// a lost lease.
	got, _ := base.Get(context.Background(), "evt_lease_lost")
	if got.Status != stripenav.StatusPending {
		t.Errorf("status = %s, want Pending (lifecycle should have abandoned the row)", got.Status)
	}
	if got.TransactionID != "" {
		t.Errorf("TransactionID = %q, want empty (no successful submit)", got.TransactionID)
	}
}

// TestWorker_NonRetriableErrorRejects: first attempt fails with a
// non-retriable error → row moves to rejected immediately.
func TestWorker_NonRetriableErrorRejects(t *testing.T) {
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{}, &nav.NAVError{HTTPStatus: 400, Code: "INVALID_REQUEST", Retriable: false, Message: "bad"}
		},
	}
	w, store := newTestWorker(t, client, time.Now)
	now := time.Now().UTC()
	if err := store.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_bad",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now.Add(-time.Second),
		RawEvent:      sampleInvoiceData(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "evt_bad")
	if got.Status != stripenav.StatusRejected {
		t.Fatalf("expected rejected, got %s", got.Status)
	}
}
