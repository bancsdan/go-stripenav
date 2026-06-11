# Embedding gostripenav as a Go library

If your backend is already a Go service, you can mount the bridge as
just another `http.Handler` on your existing `net/http` (or chi, gin,
echo, etc.) router. The standalone container packaging of the same
library lives in the
[stripenav service repo](https://github.com/bancsdan/stripenav) — this
page covers the in-process integration.

When to embed vs. deploy the container:

| | Embed | Container |
| --- | --- | --- |
| Your backend is Go | recommended | works but adds an extra hop |
| Your backend isn't Go | impossible | the only option |
| You already have a webhook endpoint pattern | reuse it | duplicate ops |
| You want a shared `http.Client` / logger / tracer | embed | sidecar lifecycle |
| You want a real, durable `SubmissionStore` | embed and bring your own | the service repo ships a Postgres adapter |

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
                // Street + number. Required: NAV's simpleAddress
                // demands a non-blank additionalAddressDetail, and the
                // mapper rejects an incomplete supplier address.
                AdditionalDetail: "Fő utca 1.",
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
against durable storage:

```go
type SubmissionStore interface {
    Put(ctx context.Context, s Submission) error
    Get(ctx context.Context, eventID string) (Submission, error)
    UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error
    ClaimBatch(ctx context.Context, claimer string, limit int, lease time.Duration) ([]Submission, error)
    RenewClaim(ctx context.Context, eventID, claimer string, lease time.Duration) error
    ReleaseClaim(ctx context.Context, eventID, claimer string) error
    FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
```

Contract highlights (the full rules are on the interface's godoc):

- `Put` must fail for an existing `EventID`, wrapping
  `stripenav.ErrAlreadyExists` so the handler can tell a duplicate
  delivery from a store outage.
- `ClaimBatch` must hand each due row to at most one claimer, with a
  TTL lease — and must not return rows whose lease is still valid,
  even when the holder is the caller itself.
- `RenewClaim`/`ReleaseClaim` return `stripenav.ErrClaimLost` when the
  claim is no longer held.

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
    raw_event         BYTEA NOT NULL,
    claimed_by        TEXT NOT NULL DEFAULT '',
    claimed_until     TIMESTAMPTZ
);

CREATE INDEX stripenav_submissions_invoice_number_idx
    ON stripenav_submissions(invoice_number);

CREATE INDEX stripenav_submissions_claimable_idx
    ON stripenav_submissions(next_attempt_at)
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

`ClaimBatch` is where multi-replica safety lives: implement it as
`UPDATE … FROM (SELECT … FOR UPDATE SKIP LOCKED)` setting
`claimed_by`/`claimed_until`, and replicas scale horizontally without
any extra coordination.

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
    // statuses: "accepted", "rejected", "aborted" (parent terminally
    // failed), plus the "deadline_exceeded" alarm event emitted when a
    // still-unreported submission crosses NAV's 24-hour window.
    RecordSubmissionResult(status string)
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

Every replica can safely run its worker: rows are claimed through
`SubmissionStore.ClaimBatch` with a TTL lease, so replicas process
disjoint work and a crashed replica's claims become available again
after the lease expires. No leader election or external coordination is
needed — but your store implementation must honour the `ClaimBatch`
contract above (the bundled in-memory store and the service repo's
Postgres adapter both do).

One caveat at scale: NAV rate-limits per source IP, and replicas behind
one NAT gateway share an IP — see the README's "Multi-replica
deployments" section for how to split the rate budget.

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
- The [stripenav service repo](https://github.com/bancsdan/stripenav) —
  standalone container packaging of this library (env-var config,
  Postgres store) if you don't want to embed.
