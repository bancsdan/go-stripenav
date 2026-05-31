# go-stripenav

[![Go Reference](https://pkg.go.dev/badge/github.com/bancsdan/go-stripenav.svg)](https://pkg.go.dev/github.com/bancsdan/go-stripenav)
[![CI](https://github.com/bancsdan/go-stripenav/actions/workflows/ci.yml/badge.svg)](https://github.com/bancsdan/go-stripenav/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/bancsdan/go-stripenav)](https://github.com/bancsdan/go-stripenav/releases)
[![Container](https://img.shields.io/badge/ghcr.io-bancsdan%2Fgo--stripenav-blue?logo=docker)](https://github.com/bancsdan/go-stripenav/pkgs/container/go-stripenav)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

Bridge Stripe webhook events to Hungary's NAV [Online Számla v3.0](https://onlineszamla.nav.gov.hu/) invoice reporting API.

Stripe is the dominant billing platform for online merchants, but it does not natively report invoices to Hungary's tax authority. Hungarian VAT law requires invoices to be reported to NAV within **24 hours** of issuance (and within **4 hours** for invoices issued by automated systems, which Stripe is). This project fills that gap so a Stripe-native backend can comply without re-issuing every invoice through a third-party invoicing service like szamlazz.hu or Billingo.

> Status: `v0.x`. The public API is first-cut and may evolve before `v1.0.0`.

## Two ways to use it

| If your backend is… | Use the |
| --- | --- |
| Go | **library** — import `github.com/bancsdan/go-stripenav`, mount the handler on your existing HTTP server. See [`docs/EMBED.md`](./docs/EMBED.md). |
| Node, PHP, Python, .NET, Ruby, or anything else | **container** — run `ghcr.io/bancsdan/go-stripenav`, point Stripe's webhook at it. See [`docs/DEPLOY.md`](./docs/DEPLOY.md). |

Both paths run the same code: the container is the library wrapped in a thin `cmd/gostripenav` binary.

## What it does

- Exposes an embeddable `http.Handler` (Go library) or a deployable HTTP service (container) that accepts Stripe webhook events on `/webhooks/stripe`.
- Verifies the Stripe webhook signature.
- Routes Stripe lifecycle events to the matching NAV `manageInvoice` operation:
  - `invoice.finalized` → **CREATE**
  - `invoice.voided` / `invoice.marked_uncollectible` → **STORNO**
  - `credit_note.created` / `credit_note.voided` → **MODIFY**
- Implements the full NAV v3.0 envelope: signed `tokenExchange`, `manageInvoice`, `manageAnnulment`, and `queryTransactionStatus`.
- Persists each submission through a pluggable `SubmissionStore`. Ships with an in-memory reference store (dev only) and a production Postgres adapter in the container.
- Runs a background worker that polls transaction status, retries transient failures with exponential backoff, respects parent-child ordering (STORNO waits for its CREATE to be accepted), and aborts submissions that miss the 24-hour deadline.
- Out-of-band `(*BridgeHandler).AnnulInvoice` method for the rare "I sent NAV malformed data" case — never triggered by Stripe events, called manually from your admin tooling.

## Container quickstart

```bash
docker run --rm -p 8080:8080 --env-file .env \
  ghcr.io/bancsdan/go-stripenav:latest
```

With `.env` populated per [`docs/DEPLOY.md`](./docs/DEPLOY.md). Then point your Stripe webhook at `https://your-host:8080/webhooks/stripe` and subscribe to `invoice.finalized`, `invoice.voided`, `invoice.marked_uncollectible`, `credit_note.created`, `credit_note.voided`.

## Library quickstart

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
            BaseURL:     nav.TestBaseURL, // nav.ProductionBaseURL when you're ready
            Login:       "…",
            Password:    "…",
            TaxNumber:   "12345678",        // accepts hyphenated 12345678-9-01 too
            SignKey:     "…",
            ExchangeKey: "0123456789ABCDEF", // exactly 16 chars
            Software: nav.Software{
                ID:             "HU00000000GOSTRPNV",
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
        Store: storeinmem.New(), // dev only — implement SubmissionStore against your DB for production
    })
    if err != nil {
        log.Fatal(err)
    }
    http.Handle("/webhooks/stripe", h)
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

For production, implement `stripenav.SubmissionStore` against your database. See [`docs/EMBED.md`](./docs/EMBED.md) for the SubmissionStore contract, a Postgres sketch, and how to wire metrics, custom loggers, and out-of-band annulment.

## Configuration reference

| Field | Purpose |
| --- | --- |
| `StripeWebhookSecret` | Stripe endpoint signing secret (`whsec_…`). Required. |
| `NAV` | NAV technical-user credentials and software identification. Required. |
| `Supplier` | The supplier's NAV-registered identity. Required. |
| `Store` | Pluggable submission persistence. Required. |
| `ExchangeRateProvider` | Function returning the foreign→HUF rate for a given currency. Required if non-HUF invoices are expected; if nil, non-HUF invoices fail mapping. |
| `Logger` | `*slog.Logger`. Defaults to `slog.Default()`. |
| `Metrics` | `MetricsRecorder` for per-status counters and per-call latency. Optional. |
| `Clock` | Time source override (useful in tests). Defaults to `time.Now`. |
| `AcceptTimeout` | Max time the handler spends in mapping + persisting per webhook delivery. Defaults to 5s. |
| `DisableWorker` | If true, do not start the background worker (e.g. when you run the worker in a separate process). Defaults to false. |

Test plumbing is via the `WithNAVClient(c NAVClient)` functional option so the public Config struct stays focused on production fields.

## Operational notes

### 24-hour SLA

The worker treats the 24-hour reporting deadline as a hard cap. Submissions whose `IssuedAt` is more than 24 hours in the past are moved to the `aborted` terminal state with an error-level log and a metric. Wire those logs into your oncall.

### Persistence

The bundled `storeinmem.Store` loses state on process restart — unit tests and local dev only. In production:

- **Embed the library**: implement `stripenav.SubmissionStore` against your database (see [`docs/EMBED.md`](./docs/EMBED.md)).
- **Run the container**: set `STORE_URL=postgres://...` to use the built-in Postgres adapter; the container ships migrations and runs them on boot.

The interface:

```go
type SubmissionStore interface {
    Put(ctx context.Context, s Submission) error
    Get(ctx context.Context, eventID string) (Submission, error)
    UpdateStatus(ctx context.Context, eventID string, mut func(*Submission) error) error
    ListPending(ctx context.Context, before time.Time, limit int) ([]Submission, error)
    FindByInvoiceNumber(ctx context.Context, invoiceNumber string) ([]Submission, error)
}
```

`UpdateStatus` must be atomic. The Postgres adapter implements it as a `SELECT … FOR UPDATE` inside a transaction.

### Retries and backoff

The worker retries transient NAV failures with exponential backoff (`base=30s`, `cap=15m`, ±20% jitter). Non-retriable NAV errors (e.g. `INVALID_USER_RIGHT`, `INVALID_REQUEST`) cause the submission to move to `rejected` without retry. Parent dependencies are enforced: a STORNO submission waits until its CREATE has been accepted by NAV before going on the wire.

### Environment selection

NAV has separate production and test endpoints. The client refuses to default — you must pass `nav.ProductionBaseURL` or `nav.TestBaseURL` explicitly so a misconfigured deployment never accidentally hits production.

## What this package does NOT do

- **Inbound (purchase) invoice reporting** via `INBOUND` query operations.
- **Periodic taxpayer queries** (`queryTaxpayer`) to validate counterparties.
- **PDF or e-invoice generation.** Stripe issues the invoice; this package only reports it.
- **Tax determination.** Stripe-computed line VAT is reformatted, not recomputed. If your Stripe Tax setup is wrong, NAV will reject the submission.
- **Schemas other than NAV Online Számla v3.0.** EU PEPPOL / UBL / FatturaPA are out of scope.
- **Hosted SaaS.** You run the binary (or import the library); the project is not a managed service.

## Repository layout

```
github.com/bancsdan/go-stripenav
├── stripenav.go            // Handler + Config + Shutdown + AnnulInvoice
├── submission.go           // Submission, SubmissionStore, state machine
├── worker.go               // background worker, retries, deadline, parent ordering
├── credit_note.go          // CreditNote → MODIFY synthesis; STORNO line negation
├── mapping/                // Stripe → NAV translation (pure, no I/O)
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
├── storeinmem/             // reference SubmissionStore (dev only)
├── cmd/                    // nested module — binaries, not part of the library
│   ├── gostripenav/        // the canonical service, what the container runs
│   └── nav-status/         // operator CLI for queryTransactionStatus
├── docs/
│   ├── DEPLOY.md           // container deployment guide
│   ├── EMBED.md            // library embedding guide
│   └── nav-api-samples/    // NAV-published reference XMLs
├── Dockerfile              // multi-stage build, distroless, ~9 MB final
└── Taskfile.yml            // dev workflows: task dev, task nav:status, etc.
```

Library users (`go get`) pull only the root packages — `cmd/`, `Dockerfile`, `docs/`, and the GitHub Actions workflows live in nested modules or non-Go directories that the module proxy doesn't include.

## Local development

```bash
# One-time setup
brew install go-task stripe/stripe-cli/stripe jq
stripe login
task stripe:secret >> .env   # paste into STRIPE_WEBHOOK_SECRET

# Three terminals
task stripe:listen           # Stripe CLI forwards events to localhost:8080
task dev                     # the bridge, same code as the container
task stripe:trigger EVENT=invoice.finalized   # fire test events

# Inspect a NAV transaction
task nav:status TX=<transactionId>
```

See `task --list` for the full task surface.

## Testing against NAV's test environment

The package ships unit tests covering signing, mapping, lifecycle, and handler behaviour. End-to-end testing against `api-test.onlineszamla.nav.gov.hu` requires a NAV technical user — register one at the [test portal](https://onlineszamla-test.nav.gov.hu/), set the `NAV_*` env vars in `.env`, and run `task dev` plus `task stripe:trigger`.

## License

MIT — see [`LICENSE`](./LICENSE).
