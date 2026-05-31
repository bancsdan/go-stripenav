# Embedding gostripenav as a Go library

If your backend is already a Go service, you can mount the bridge as
just another `http.Handler` on your existing `net/http` (or chi, gin,
echo, etc.) router. The container in
[`DEPLOY.md`](./DEPLOY.md) is one specific way to package the same
library — this page covers the in-process integration.

When to embed vs. deploy the container:

| | Embed | Container |
| --- | --- | --- |
| Your backend is Go | recommended | works but adds an extra hop |
| Your backend isn't Go | impossible | the only option |
| You already have a webhook endpoint pattern | reuse it | duplicate ops |
| You want a shared `http.Client` / logger / tracer | embed | sidecar lifecycle |
| You want a real, durable `SubmissionStore` | embed and bring your own | container ships only the in-memory store |

## Minimum integration

```go
package main

import (
    "log"
    "net/http"

    stripenav "github.com/bancsdan/go-stripenav"
    "github.com/bancsdan/go-stripenav/mapping"
    "github.com/bancsdan/go-stripenav/nav"
)

func main() {
    h, err := stripenav.Handler(stripenav.Config{
        StripeWebhookSecret: "whsec_…",
        NAV: nav.Config{
            BaseURL:     nav.TestBaseURL, // nav.ProductionBaseURL when you're ready
            Login:       "…",
            Password:    "…",
            TaxNumber:   "12345678",
            SignKey:     "…",
            ExchangeKey: "0123456789ABCDEF",
            Software: nav.Software{
                ID:             "HU00000000GOSTRPNV", // your real software id
                Name:           "MyShop",
                Operation:      "LOCAL_SOFTWARE",
                MainVersion:    "1.0.0",
                DevName:        "MyShop Kft.",
                DevContact:     "tech@myshop.hu",
                DevCountryCode: "HU",
            },
        },
        Supplier: mapping.Supplier{
            TaxNumber: "12345678-9-01",
            Name:      "MyShop Kft.",
            Address: mapping.Address{
                CountryCode: "HU",
                PostalCode:  "1011",
                City:        "Budapest",
            },
        },
        Store: myProductionStore, // see "Implementing SubmissionStore" below
    })
    if err != nil {
        log.Fatal(err)
    }

    mux := http.NewServeMux()
    mux.Handle("/webhooks/stripe", h)
    // … your other routes …
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

That's the entire integration. The handler verifies Stripe signatures,
dedupes events, persists submissions, and returns 200 OK. A background
worker (started by `Handler` unless `Config.DisableWorker` is set)
drives each submission through NAV.

## Implementing `SubmissionStore`

Production deployments must implement `stripenav.SubmissionStore`
against durable storage. The interface is small:

```go
type SubmissionStore interface {
    Put(ctx context.Context, s Submission) error
    Get(ctx context.Context, eventID string) (Submission, error)
    UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error
    ListPending(ctx context.Context, before time.Time, limit int) ([]Submission, error)
    FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
```

A Postgres sketch:

```sql
CREATE TABLE stripenav_submissions (
    event_id          TEXT PRIMARY KEY,
    kind              TEXT NOT NULL,
    operation         TEXT NOT NULL,
    invoice_number    TEXT NOT NULL,
    parent_event_id   TEXT,
    status            TEXT NOT NULL,
    attempts          INT NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    transaction_id    TEXT NOT NULL DEFAULT '',
    next_attempt_at   TIMESTAMPTZ NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    raw_event         BYTEA NOT NULL
);

CREATE INDEX stripenav_submissions_invoice_number_idx
    ON stripenav_submissions(invoice_number);

CREATE INDEX stripenav_submissions_pending_idx
    ON stripenav_submissions(status, next_attempt_at)
    WHERE status IN ('pending', 'submitted', 'processing');
```

`UpdateStatus` should be implemented as a single transaction:

```go
func (s *PgStore) UpdateStatus(ctx context.Context, eventID string,
    mut func(*stripenav.Submission) error) error {

    return pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{}, func(tx pgx.Tx) error {
        var sub stripenav.Submission
        if err := tx.QueryRow(ctx,
            `SELECT … FROM stripenav_submissions WHERE event_id = $1 FOR UPDATE`,
            eventID).Scan(&sub.EventID /* ... */); err != nil {
            return err
        }
        if err := mut(&sub); err != nil {
            return err
        }
        _, err := tx.Exec(ctx,
            `UPDATE stripenav_submissions SET … WHERE event_id = $1`,
            eventID /* ... */)
        return err
    })
}
```

`ListPending` should use `SELECT … FOR UPDATE SKIP LOCKED` if you want
to scale workers horizontally without coordination.

## Logging and metrics

```go
import "log/slog"

cfg := stripenav.Config{
    // …
    Logger: slog.New(yourHandler),  // *slog.Logger; defaults to slog.Default()
    Metrics: yourMetricsRecorder,    // implements stripenav.MetricsRecorder
}
```

`MetricsRecorder` is two methods:

```go
type MetricsRecorder interface {
    RecordSubmissionResult(status string)              // "accepted", "rejected", "aborted"
    RecordLatency(op string, d time.Duration)          // "submit", "status"
}
```

Plug into Prometheus by incrementing counters in your implementation.

## Out-of-band annulment

Stripe webhooks never trigger NAV ANNUL — that's reserved for "the
report I sent NAV was malformed" situations Stripe can't observe. When
you discover such a case, call it from your admin tooling:

```go
txID, err := h.AnnulInvoice(ctx, "INV-2026-0042", "wrong customer tax id")
if err != nil {
    return err
}
// txID is the NAV transactionId for the annulment.
// NAV will queue it as VERIFICATION_PENDING and require manual
// approval in the NAV portal before applying it.
```

## Worker lifecycle

If you run multiple replicas of your service, only one worker should
process each submission at a time. Two options:

1. Run the worker in one replica (set `Config.DisableWorker = true`
   everywhere else, run one separate "worker pod"). Cleanest.
2. Let every replica run a worker and rely on the store's
   `FindByInvoiceNumber`/`UpdateStatus` to claim work atomically
   (`FOR UPDATE SKIP LOCKED`). More complex; needed only at scale.

Until you reach that scale, option 1 is fine.

## Graceful shutdown

`stripenav.Handler` returns a `*BridgeHandler` with a `Shutdown(ctx)`
method that stops the background worker. Wire it into your shutdown
sequence:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := h.Shutdown(ctx); err != nil {
    log.Printf("bridge shutdown: %v", err)
}
```

## See also

- `pkg.go.dev/github.com/bancsdan/go-stripenav` — full API reference.
- `cmd/gostripenav/main.go` — the in-tree binary that wires everything
  with env-var config. Same code as the container image.
- `docs/DEPLOY.md` — the container deployment path if you don't want to
  embed.
