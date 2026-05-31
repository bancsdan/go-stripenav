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
  200. A background worker polls the store, submits to NAV, polls
  transaction status, and retries transient failures with exponential
  backoff (`30s` base, `15m` cap, ±20% jitter).
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
    ListPending(ctx context.Context, before time.Time, limit int) ([]Submission, error)
    FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
```

`UpdateStatus` must be atomic — the simplest implementation uses
`SELECT … FOR UPDATE` inside a transaction.

A working Postgres adapter (with embedded migration, atomic updates,
multi-worker concurrency tests) lives in the
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

## Testing against NAV's test environment

Unit tests cover signing, mapping, lifecycle, and handler behaviour
(`go test ./... -race`). End-to-end tests against
`api-test.onlineszamla.nav.gov.hu` are not part of the default run —
register a test technical user on the NAV portal, set `nav.Config`
appropriately, and replay a captured Stripe event through the handler.

The [stripenav service repo](https://github.com/bancsdan/stripenav)
ships task targets (`task stripe:scenario:void`,
`task stripe:trigger EVENT=invoice.finalized`) that drive this
end-to-end against the local container + the Stripe CLI.

## License

MIT — see [LICENSE](LICENSE).
