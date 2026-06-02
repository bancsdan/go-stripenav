# go-stripenav

[![Go Reference](https://pkg.go.dev/badge/github.com/bancsdan/go-stripenav.svg)](https://pkg.go.dev/github.com/bancsdan/go-stripenav)
[![CI](https://github.com/bancsdan/go-stripenav/actions/workflows/ci.yml/badge.svg)](https://github.com/bancsdan/go-stripenav/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/bancsdan/go-stripenav)](https://github.com/bancsdan/go-stripenav/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

Go library that bridges Stripe webhook events to Hungary's NAV
[Online Számla v3.0](https://onlineszamla.nav.gov.hu/) invoice reporting API.

Embed it in your existing Go backend to satisfy NAV's mandatory invoice
reporting (24 hours from issuance, 4 hours for automated systems) without
re-issuing every Stripe invoice through a third-party invoicing service.

**Don't write Go?** A ready-to-deploy container of the same logic lives at
[bancsdan/stripenav](https://github.com/bancsdan/stripenav). Run the
container, point your Stripe webhook at it, done.

## Install

```bash
go get github.com/bancsdan/go-stripenav
```

## Quickstart

```go
package main

import (
    "log"
    "net/http"

    stripenav "github.com/bancsdan/go-stripenav"
    "github.com/bancsdan/go-stripenav/mapping"
    "github.com/bancsdan/go-stripenav/nav"
    "github.com/bancsdan/go-stripenav/storeinmem"
)

func main() {
    h, err := stripenav.Handler(stripenav.Config{
        StripeWebhookSecret: "whsec_…",
        NAV: nav.Config{
            BaseURL:     nav.TestBaseURL, // or nav.ProductionBaseURL
            Login:       "…",
            Password:    "…",
            TaxNumber:   "12345678",
            SignKey:     "…",
            ExchangeKey: "0123456789ABCDEF",
            Software: nav.Software{
                ID:             "HU12345678MYSHOP01",
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
        Store: storeinmem.New(), // dev only — implement SubmissionStore for production
    })
    if err != nil {
        log.Fatal(err)
    }
    http.Handle("/webhooks/stripe", h)
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

On the Stripe side, register a webhook endpoint at
`https://your-host/webhooks/stripe` and subscribe to:

- `invoice.finalized`
- `invoice.voided`
- `invoice.marked_uncollectible`
- `credit_note.created`
- `credit_note.voided`

## What it does

| | Mapped to |
| --- | --- |
| `invoice.finalized` | NAV `manageInvoice` operation `CREATE` |
| `invoice.voided`, `invoice.marked_uncollectible` | NAV `manageInvoice` operation `STORNO` (mirror invoice with negative amounts referencing the original) |
| `credit_note.created`, `credit_note.voided` | NAV `manageInvoice` operation `MODIFY` |
| out-of-band admin call | NAV `manageAnnulment` via `(*BridgeHandler).AnnulInvoice` |

Implementation details:

- **Signature verification** on every Stripe delivery using the documented
  HMAC-SHA256 scheme.
- **Idempotency** on `event.id` so Stripe re-deliveries don't produce
  duplicate NAV submissions.
- **NAV v3.0 envelope**: signed `tokenExchange`, `manageInvoice`,
  `manageAnnulment`, `queryTransactionStatus`. Includes SHA-512 password
  hash, SHA3-512 request signature, AES-128-ECB exchange-token
  decryption with PKCS#7 unpadding.
- **Persist-then-async**: the webhook handler verifies, persists, returns
  200, and signals the worker. The worker submits to NAV, polls
  transaction status, and retries transient failures with exponential
  backoff (`30s` base, `15m` cap, ±20% jitter).
- **Wakeup-or-sleep pacing**: the worker reacts to handler signals
  immediately and otherwise wakes on a bounded sleep — no fixed-tick
  polling. Each claimed row is driven by its own short-lived goroutine.
- **Multi-replica safe**: rows are claimed via
  `SELECT … FOR UPDATE SKIP LOCKED` with a TTL lease, so multiple
  replicas process disjoint work and crashed claims are recovered
  automatically.
- **Parent dependency tracking**: a STORNO submission waits for its
  parent CREATE to be `accepted` on NAV's side before submitting.
- **24-hour deadline**: submissions that miss it transition to `aborted`
  with an error-level log.

## Configuration

```go
type Config struct {
    StripeWebhookSecret  string
    NAV                  nav.Config
    Supplier             mapping.Supplier
    Store                SubmissionStore
    ExchangeRateProvider func(ctx context.Context, currency string, at time.Time) (string, error)
    Logger               *slog.Logger    // defaults to slog.Default()
    Metrics              MetricsRecorder // optional
    Clock                func() time.Time // defaults to time.Now
    AcceptTimeout        time.Duration   // bounds the persist work; defaults to 5s
    DisableWorker        bool

    // Worker pacing — leave at zero for sensible defaults.
    WorkerMaxSleep       time.Duration  // bound between store scans; default 10s
    WorkerPollInterval   time.Duration  // gap between NAV status polls; default 5s
    WorkerLeaseDuration  time.Duration  // claim TTL; default 60s
    WorkerClaimerID      string         // identifier; defaults to hostname + random suffix
    // unexported test injection seam — use stripenav.WithNAVClient(fake)
}
```

Test injection:

```go
h, err := stripenav.Handler(cfg, stripenav.WithNAVClient(fakeClient))
```

## Persistence

The bundled `storeinmem.Store` is a sync.Mutex-protected map. **State is
lost on restart** — fine for unit tests and local dev, not for production.

For production, implement `stripenav.SubmissionStore` against your
durable storage:

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

`ClaimBatch` is the multi-replica safety primitive: it must hand out
each row to at most one caller and grant a TTL lease so a crashed
claimer's work becomes claimable again after the lease expires. The
canonical Postgres implementation is
`UPDATE … FROM (SELECT … FOR UPDATE SKIP LOCKED)`. `UpdateStatus` must
be atomic with respect to concurrent claims on the same row. Methods
that target a specific claim (`RenewClaim`, `ReleaseClaim`) must
return `stripenav.ErrClaimLost` if the claim is no longer held.

A working Postgres adapter (with embedded migration, the canonical
`SKIP LOCKED` claim query, and a multi-claimer concurrency test) lives
in the
[stripenav service repo](https://github.com/bancsdan/stripenav/tree/main/internal/storepg).
Copy or vendor it if you want the same shape.

## Package layout

```
github.com/bancsdan/go-stripenav
├── stripenav.go            // Handler + Config + Shutdown + AnnulInvoice
├── submission.go           // Submission, SubmissionStore, state machine
├── worker.go               // background worker, retries, deadline, parent deps
├── credit_note.go          // invoice → storno + credit-note synthesis
├── mapping/                // Stripe → NAV translation (pure, no I/O)
│   ├── mapping.go
│   ├── tax.go              // Hungarian VAT-number splitting, customer category
│   ├── currency.go         // big.Rat amounts, HUF summary
│   └── errors.go
├── nav/                    // NAV API client
│   ├── client.go           // tokenExchange, manageInvoice, manageAnnulment, queryTransactionStatus
│   ├── envelope.go         // common:header, common:user, software
│   ├── sign.go             // SHA-512 password hash, SHA3-512 request signature
│   ├── token.go            // AES-128-ECB + PKCS#7
│   ├── requests.go         // per-endpoint request envelopes
│   ├── errors.go           // *NAVError with retriability
│   └── schemas/            // hand-written OSA/3.0/data, OSA/3.0/api, etc.
├── storeinmem/             // reference SubmissionStore for tests + dev
├── e2e/                    // end-to-end harness against real NAV test env
│                           // (//go:build navtest — skipped in default test run)
└── docs/
    └── nav-api-samples/    // NAV-published sample requests for reference
```

## What this package does NOT do

- **Inbound (purchase) invoice reporting** via `INBOUND` query operations.
- **Periodic taxpayer queries** (`queryTaxpayer`) to validate counterparties.
- **PDF or e-invoice generation.** Stripe issues the invoice; this package
  only reports it.
- **Tax determination.** Stripe-computed line VAT is reformatted, not
  recomputed. If your Stripe Tax setup is wrong, NAV will reject the
  submission.
- **Schemas other than NAV Online Számla v3.0.** EU PEPPOL / UBL /
  FatturaPA are out of scope.
- **Shipping a production-grade `SubmissionStore`.** The
  [stripenav service repo](https://github.com/bancsdan/stripenav)
  packages a Postgres adapter that's ready to use.

## Testing

Unit tests cover signing, mapping, lifecycle, and handler behaviour:

```bash
task test         # go test ./... -count=1
task test:race    # with -race
```

End-to-end tests against `api-test.onlineszamla.nav.gov.hu` live in the
`e2e/` package and are gated behind the `navtest` build tag, so normal
`go test ./...` never touches NAV. The harness signs a Stripe payload,
posts it to a real `BridgeHandler`, waits for the background worker to
reach `accepted`, and asserts that NAV returned a transaction id.

To run locally:

1. Register a technical user on the NAV portal, copy the credentials.
2. `cp e2e/.env.example e2e/.env` and fill in the values (`e2e/.env`
   is gitignored).
3. `task test:e2e` — the Taskfile target loads `e2e/.env` and runs
   `go test -tags=navtest -count=1 -v ./e2e/...`.

In CI the same env vars are injected from repository secrets, so no
`.env` file is required. The `.github/workflows/ci.yml` workflow runs
the e2e job on every push to `main`, on every PR (skipping cleanly if
the fork doesn't have access to secrets), and on manual
`workflow_dispatch`.

The [stripenav service repo](https://github.com/bancsdan/stripenav)
ships additional task targets (`task stripe:scenario:void`,
`task stripe:trigger EVENT=invoice.finalized`) that drive Stripe-side
scenarios against a locally-running container + the Stripe CLI.

## License

MIT — see [LICENSE](LICENSE).
