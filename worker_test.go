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
		TickInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w, store
}

func TestWorker_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	tickCount := int32(0)
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{TransactionID: "T1"}, nil
		},
		statusFn: func(ctx context.Context, tx string, _ bool) (schemas.QueryTransactionStatusResponse, error) {
			n := atomic.AddInt32(&tickCount, 1)
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
	clockNow := now
	clock := func() time.Time { return clockNow }
	w, store := newTestWorker(t, client, clock)

	sub := stripenav.Submission{
		EventID:       "evt_happy",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now,
		RawEvent:      sampleInvoiceData(),
	}
	if err := store.Put(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	got, _ := store.Get(context.Background(), "evt_happy")
	if got.Status != stripenav.StatusSubmitted || got.TransactionID != "T1" {
		t.Fatalf("after submit: %+v", got)
	}

	clockNow = clockNow.Add(time.Minute)
	_ = store.UpdateStatus(context.Background(), got.EventID, func(s *stripenav.Submission) error {
		s.NextAttemptAt = clockNow.Add(-time.Second)
		return nil
	})

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	got, _ = store.Get(context.Background(), "evt_happy")
	if got.Status != stripenav.StatusProcessing {
		t.Fatalf("after first poll: status=%s", got.Status)
	}

	clockNow = clockNow.Add(time.Minute)
	_ = store.UpdateStatus(context.Background(), got.EventID, func(s *stripenav.Submission) error {
		s.NextAttemptAt = clockNow.Add(-time.Second)
		return nil
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick3: %v", err)
	}
	got, _ = store.Get(context.Background(), "evt_happy")
	if got.Status != stripenav.StatusAccepted {
		t.Fatalf("final status = %s, want accepted", got.Status)
	}
}

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
		NextAttemptAt: clockNow,
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

func TestWorker_TransientFailureRetries(t *testing.T) {
	now := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	var calls int32
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nav.SubmitResult{}, &nav.NAVError{HTTPStatus: 500, Code: "INTERNAL_ERROR", Retriable: true, Message: "boom"}
			}
			return nav.SubmitResult{TransactionID: "T2"}, nil
		},
	}
	clockNow := now
	w, store := newTestWorker(t, client, func() time.Time { return clockNow })
	if err := store.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_retry",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now,
		RawEvent:      sampleInvoiceData(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), "evt_retry")
	if got.Status != stripenav.StatusPending || got.Attempts != 1 {
		t.Fatalf("after failure: %+v", got)
	}
	if !got.NextAttemptAt.After(now) {
		t.Fatalf("NextAttemptAt should grow after failure, got %s vs %s", got.NextAttemptAt, now)
	}

	clockNow = got.NextAttemptAt
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(context.Background(), "evt_retry")
	if got.Status != stripenav.StatusSubmitted || got.TransactionID != "T2" {
		t.Fatalf("after retry: %+v", got)
	}
}

func TestWorker_NonRetriableErrorRejects(t *testing.T) {
	now := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	client := &fakeNAVClient{
		submitFn: func(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{}, &nav.NAVError{HTTPStatus: 400, Code: "INVALID_REQUEST", Retriable: false, Message: "bad"}
		},
	}
	w, store := newTestWorker(t, client, func() time.Time { return now })
	if err := store.Put(context.Background(), stripenav.Submission{
		EventID:       "evt_bad",
		Kind:          stripenav.KindInvoice,
		Status:        stripenav.StatusPending,
		IssuedAt:      now,
		CreatedAt:     now,
		NextAttemptAt: now,
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
