# go-stripenav

Bridge Stripe webhook events to Hungary's NAV [Online Számla v3.0](https://onlineszamla.nav.gov.hu/) invoice reporting API.

Stripe is the dominant billing platform for online merchants, but it does not natively report invoices to Hungary's tax authority. Hungarian VAT law requires invoices to be reported to NAV within **24 hours** of issuance (and within **4 hours** for invoices issued by automated systems, which Stripe is). This package fills that gap so a Stripe-native backend can comply without re-issuing every invoice through a third-party invoicing service like szamlazz.hu or billingo.

> Status: `v0.x`. The public API is first-cut and may evolve before `v1.0.0`.

## Who this is for

You are running a Hungarian VAT-registered business that bills customers through Stripe and need to satisfy NAV's online invoice reporting requirement. You want a small Go library you can drop into your existing backend rather than a hosted service.

## What it does

- Exposes a single `http.Handler` you mount on your backend and register as a Stripe webhook target.
- Verifies the Stripe webhook signature.
- Translates Stripe `Invoice` and `CreditNote` events into NAV `InvoiceData` / `InvoiceAnnulment` XML.
- Handles the NAV v3.0 envelope: signed `tokenExchange`, `manageInvoice`, `manageAnnulment`, and `queryTransactionStatus`.
- Persists each submission through a pluggable `SubmissionStore` so retries survive process restarts.
- Runs a background worker that polls transaction status, retries transient failures with exponential backoff, and gives up on submissions that miss the 24-hour deadline.

## Quickstart

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
            BaseURL:     nav.TestBaseURL, // or nav.ProductionBaseURL
            Login:       "…",
            Password:    "…",
            TaxNumber:   "11111111",
            SignKey:     "…",            // 32 chars
            ExchangeKey: "0123456789ABCDEF", // 16 chars (AES-128)
            Software: nav.Software{
                ID:             "MYSOFT0000000001",
                Name:           "MyShop",
                Operation:      "LOCAL_SOFTWARE",
                MainVersion:    "1.0.0",
                DevName:        "MyShop Kft.",
                DevContact:     "tech@myshop.hu",
                DevCountryCode: "HU",
            },
        },
        Supplier: mapping.Supplier{
            TaxNumber: "12345678-9-01", // your 11-char Hungarian VAT number
            Name:      "MyShop Kft.",
            Address: mapping.Address{
                CountryCode: "HU",
                PostalCode:  "1011",
                City:        "Budapest",
            },
        },
        Store: stripenav.NewInMemoryStore(), // see "Persistence" below
    })
    if err != nil {
        log.Fatal(err)
    }
    http.Handle("/webhooks/stripe", h)
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

On the Stripe side, create a webhook endpoint pointed at `https://your-host/webhooks/stripe` and subscribe to at least `invoice.finalized`, `invoice.voided`, `invoice.marked_uncollectible`, `credit_note.created`, `credit_note.voided`.

A complete worked example using `net/http`, env-var configuration, and graceful shutdown is in [`examples/nethttp-server/`](./examples/nethttp-server/main.go).

## Configuration reference

| Field | Purpose |
| --- | --- |
| `StripeWebhookSecret` | Stripe endpoint signing secret (`whsec_…`). Required. |
| `NAV` | NAV technical-user credentials and software identification. Required. |
| `Supplier` | The supplier's NAV-registered identity. Required. |
| `Store` | Pluggable submission persistence. Required. |
| `ExchangeRateProvider` | Function returning the foreign→HUF rate for a given currency. Required if non-HUF invoices are expected. |
| `Logger` | `*slog.Logger`. Defaults to `slog.Default()`. |
| `Metrics` | `MetricsRecorder` for per-status counters and per-call latency. Optional. |
| `Clock` | Time source override (useful in tests). Defaults to `time.Now`. |
| `SyncTimeout` | Max time the handler will spend in the synchronous submit attempt before falling through to the background worker. Defaults to 5s. |
| `DisableWorker` | If true, do not start the background worker (e.g. when you run the worker in a separate process). Defaults to false. |
| `NAVClient` | Inject a custom `NAVClient` (e.g. a fake) instead of letting the handler build one from `NAV`. Optional. |

## Deployment notes

### 24-hour SLA

The package treats the 24-hour reporting deadline as a hard cap. Any submission whose `IssuedAt` is more than 24 hours in the past is moved to the `aborted` terminal state by the background worker, with an error-level log and a metric. Wire those into your oncall.

### Persistence

The bundled `InMemoryStore` loses state on process restart and is intended for unit tests and the example only. In production, implement `SubmissionStore` against your database (Postgres, MySQL, DynamoDB, …). The interface is small:

```go
type SubmissionStore interface {
    Put(ctx context.Context, s Submission) error
    Get(ctx context.Context, eventID string) (Submission, error)
    UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error
    ListPending(ctx context.Context, before time.Time, limit int) ([]Submission, error)
}
```

`UpdateStatus` must be atomic. The simplest implementation uses a `SELECT … FOR UPDATE` inside a transaction.

### Retries and backoff

The worker retries transient failures with exponential backoff (`base=30s`, `cap=15m`, ±20% jitter). Non-retriable NAV errors (e.g. `INVALID_USER_RIGHT`, `INVALID_REQUEST`) cause the submission to move to `rejected` without retry. Tune via `WorkerConfig` if you embed `Worker` directly.

### Environment selection

NAV has separate production and test environments. The client refuses to default — you must pass `nav.ProductionBaseURL` or `nav.TestBaseURL` explicitly so a misconfigured deployment never accidentally hits production.

## What this package does NOT do

- **Inbound (purchase) invoice reporting** via `INBOUND` query operations.
- **Periodic taxpayer queries** (`queryTaxpayer`) to validate counterparties.
- **PDF or e-invoice generation.** Stripe issues the invoice; this package only reports it.
- **Tax determination.** Stripe-computed line VAT is reformatted, not recomputed. If your Stripe Tax setup is wrong, NAV will reject the submission.
- **Schemas other than NAV Online Számla v3.0.** EU PEPPOL / UBL / FatturaPA are out of scope.
- **Manual annulment-by-human-decision flows.** Only programmatic `ANNUL` triggered by `invoice.voided` is supported.
- **Shipping a production-grade `SubmissionStore`.** You bring your own database adapter.

## Package layout

```
github.com/bancsdan/go-stripenav
├── stripenav.go            // Handler + Config + Shutdown
├── submission.go           // Submission, SubmissionStore, state machine
├── inmemory_store.go       // reference SubmissionStore
├── worker.go               // background worker, retries, deadline
├── credit_note.go          // CreditNote → MODIFY synthesis
├── mapping/                // Stripe → NAV translation (pure)
│   ├── mapping.go
│   ├── tax.go
│   ├── currency.go
│   └── errors.go
├── nav/                    // NAV API client
│   ├── client.go
│   ├── envelope.go
│   ├── sign.go
│   ├── token.go
│   ├── requests.go
│   ├── errors.go
│   └── schemas/            // OSA/3.0/data, OSA/3.0/api, NTCA/1.0/common types
└── examples/nethttp-server/main.go
```

## Testing against NAV's test environment

The package ships unit tests covering signing, mapping, lifecycle, and handler behavior. End-to-end integration tests against `api-test.onlineszamla.nav.gov.hu` are not included in the default test run — register a test technical user on the NAV portal, configure `nav.Config{BaseURL: nav.TestBaseURL, …}`, and replay a captured Stripe event through the handler.

## License

To be decided by the project owner before public release.
