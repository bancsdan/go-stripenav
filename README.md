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
- `credit_note.created` (see "Known credit-note limitations" below before subscribing)
- `credit_note.voided` (see "Known credit-note limitations" below before subscribing)

## What it does

| | Mapped to |
| --- | --- |
| `invoice.finalized` | NAV `manageInvoice` operation `CREATE` |
| `invoice.voided`, `invoice.marked_uncollectible` | NAV `manageInvoice` operation `STORNO` (mirror invoice with negative amounts referencing the original) |
| `credit_note.created` | NAV `manageInvoice` operation `MODIFY` — works for a single credit note against an invoice with exclusive-tax pricing; otherwise see limitations |
| `credit_note.voided` | **Currently produces a duplicate negative MODIFY instead of a reversing MODIFY — see limitations** |
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
  automatically. If lease renewal fails mid-flight (the claim was
  stolen, the store dropped the row, etc.) the lifecycle goroutine
  aborts immediately rather than continuing to write under a stale
  claim.
- **Outbound rate limiting**: every NAV API call goes through a
  per-client token-bucket limiter at 1 request/second (matching NAV's
  documented per-source-IP ceiling) with burst 1. Override via
  `nav.Config.RateLimit` / `RateBurst`, disable for tests with
  `DisableRateLimit=true`. The limiter is in-process only — see the
  multi-replica note below.
- **Hungarian calendar dates**: every date field submitted to NAV
  (`invoiceIssueDate`, `invoiceDeliveryDate`, `paymentDate`, the
  `invoiceDeliveryPeriodStart/End` pair) is rendered in a fixed UTC+2
  zone — CEST. This matches Hungarian local time for ~7 months/year
  and drifts by an hour during CET (late October through late March),
  producing at most a one-day shift for invoices finalized in the
  23:00–00:00 UTC window. The trade-off — fixed offset versus a real
  tzdata lookup — keeps the binary self-contained.
- **Parent dependency tracking**: a STORNO submission waits for its
  parent CREATE to be `accepted` on NAV's side before submitting.
- **24-hour deadline**: submissions that miss it transition to `aborted`
  with an error-level log.
- **Inclusive- and exclusive-tax pricing**: handled per Stripe line item.
  When `taxes[].tax_behavior=inclusive` (Stripe Tax's default for
  consumer-facing prices), `line.amount` is treated as the gross and
  the net is derived as `amount - vat`. Exclusive lines (the default
  when no Stripe Tax) treat `line.amount` as the net.
- **Exact-rational money arithmetic**: all monetary amounts and VAT
  rates flow through `math/big.Rat` — no `float64` in the money path.
  Rendered net/vat/gross are reconciled at each level (line, per-rate
  summary, invoice summary) so that `net + vat = gross` holds in the
  emitted strings even when independent rounding of three big.Rats
  would otherwise drift by a fillér.
- **§58 continuous-service / subscription billing**: when the Stripe
  invoice covers a service period (`period_end > period_start`), it is
  mapped as a periodic settlement (`periodicalSettlement=true`,
  `invoiceDeliveryPeriodStart/End` from `period_start/period_end`). The
  §58 tax point is set to the invoice issue date, matching the
  *advance-billing* rule — Stripe's default for
  `collection_method=charge_automatically`, where the card is charged on
  finalization at the start of the cycle. The period-span check is the
  signal (not `billing_reason`) so that quote-originated subscription
  invoices (`billing_reason=quote_accept`) are also covered.

### Multi-replica deployments and NAV's rate limit

NAV's rate limit is **per source IP**, not per technical user or per
process. In the typical cloud deployment pattern — ECS Fargate or EKS
pods running in private subnets with outbound traffic routed through
a NAT Gateway — every replica appears to NAV as the *same* IP (the
NAT Gateway's Elastic IP). Multi-AZ setups give you one EIP per AZ,
but tasks aren't pinned to AZs, so you generally can't predict which
EIP a given request will egress from. The library's built-in limiter
is per-process, so N replicas at 1 req/s collectively hit NAV at N
req/s — and NAV will start returning 429s as soon as you scale past
one instance. You have a few options, in order of pragmatism:

1. **Divide the budget per replica.** Set
   `nav.Config.RateLimit = 1.0 / N` for an N-replica deployment.
   Wasteful when some replicas are idle, and N needs updating when
   you scale, but zero new infrastructure.
2. **Distributed rate limiter.** Wrap the NAV client with a
   shared-state limiter (Redis token bucket, DynamoDB counter,
   etc.). The library doesn't ship this, but the `*http.Client` on
   `nav.Config.HTTPClient` is the right place to plug in a
   middleware that does the global enforcement.
3. **Egress sidecar.** Route NAV traffic through a single proxy
   (Envoy, HAProxy, an API gateway) that does the rate limiting
   upstream of your replicas, and set `DisableRateLimit=true` on
   every replica. Useful if you already run a service mesh.

If you're running a single replica today, the default 1 req/s is
correct and you can ignore all of this — but plan ahead before
scaling horizontally.

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
├── mapping/                // PUBLIC: Supplier, Address (Config fields only)
│   └── types.go
├── nav/                    // PUBLIC: Config, Software, base URLs, NAVError,
│   ├── config.go           //         InvoiceOperation, AnnulmentOperation,
│   ├── constants.go        //         SubmitResult, ErrBatchTooLarge
│   ├── errors.go
│   ├── types.go
│   └── schemas/            // hand-written OSA/3.0/data, OSA/3.0/api, etc.
├── internal/
│   ├── navclient/          // NAV API HTTP client implementation
│   │   ├── client.go       // tokenExchange, manageInvoice, manageAnnulment,
│   │   ├── envelope.go     //   queryTransactionStatus, request signing,
│   │   ├── sign.go         //   AES token decryption, retriability mapping
│   │   ├── token.go
│   │   ├── requests.go
│   │   └── errors.go
│   └── invoicemap/         // Stripe → NAV translation (pure, no I/O)
│       ├── mapping.go      // MapInvoice, MapOptions, Operation
│       ├── tax.go          // Hungarian VAT-number splitting, customer category
│       ├── currency.go     // big.Rat amounts, HUF summary
│       └── errors.go
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
- **Reliable credit-note handling.** The current `credit_note.*` code
  path has known bugs that misreport amounts to NAV. Until they're
  fixed, do not subscribe to these events in production unless your
  use case happens to dodge every gap below:

  1. **`credit_note.voided` is mishandled.** Both `credit_note.created`
     and `credit_note.voided` are routed through the same handler,
     which always sign-flips amounts to negative. The created event
     produces a correct negative-amount MODIFY (reducing the invoice
     total). The voided event then produces *another* negative-amount
     MODIFY instead of the positive reversing MODIFY that would undo
     the first credit. Net effect: NAV sees `original − 2×credit`
     instead of `original`. Silent data corruption.
  2. **Inclusive tax behavior is dropped on credit notes.** When the
     synthetic invoice is built from a credit note, `tax_behavior` is
     not copied. Credit notes against Stripe Tax inclusive-priced
     invoices (the B2C SaaS default) ship the wrong net/vat/gross
     split and wrong VAT percentage to NAV.
  3. **`modificationIndex` is hardcoded to 1.** A second credit note
     against the same original invoice will collide with the first
     and be rejected by NAV. The correct value is `1 + count of prior
     MODIFY/STORNO submissions against the same invoice`, which would
     require a `FindByInvoiceNumber` lookup; that lookup is not done.
  4. **`pre_payment` vs `post_payment` credit notes are not
     distinguished.** Áfa tv. §77 specifies different VAT-correction
     timing for the two types; the mapper treats them identically.
- **§58 subsequent-billing (utólagos számlázás).** Only the
  advance-billing rule is implemented — invoice issue date is taken as
  the tax point. Subsequent billing (invoice issued *after* the service
  period ends, e.g. usage-based / metered subscriptions invoiced in
  arrears) requires a different tax-point computation including the
  "+60 days from period-end" clamp, and is not handled. If you turn on
  Stripe metered billing (`usage_type=metered`) or otherwise invoice in
  arrears, the resulting NAV submissions will report the wrong tax
  point.
- **`electronicInvoiceHash`.** Optional under the current
  `completenessIndicator=false` mode — not computed or submitted. If you
  ever switch to `completenessIndicator=true` (using NAV as the invoice
  delivery channel rather than Stripe's PDF), this hash becomes
  mandatory and is not yet supported.
- **`paymentMethod` auto-derivation.** The value is caller-supplied via
  `MapOptions.PaymentMethod` (default `CARD`). It is not derived from
  `invoice.collection_method` or
  `invoice.charge.payment_method_details`. If you mix card and bank
  transfer collection on the same store, you must override per
  invoice.

## Testing

Unit tests cover signing, mapping, lifecycle, and handler behaviour:

```bash
task test         # go test ./... -count=1
task test:race    # with -race
```

End-to-end tests against `api-test.onlineszamla.nav.gov.hu` live in the
`e2e/` package and are gated behind the `navtest` build tag, so normal
`go test ./...` never touches NAV. Each test signs a Stripe payload,
posts it to a real `BridgeHandler`, waits for the background worker to
reach `accepted`, and asserts that NAV returned a transaction id.
One scenario per file:

- `invoice_finalized_test.go` — baseline HUF invoice → NAV CREATE.
- `subscription_invoice_test.go` — §58 periodic-settlement invoice
  with `periodicalSettlement=true` + delivery period dates.
- `inclusive_tax_test.go` — line with `tax_behavior=inclusive` (the
  shape Stripe Tax emits for B2C pricing).
- `domestic_customer_test.go` — Hungarian buyer with `hu_tin` →
  DOMESTIC classification + split `customerTaxNumber`.
- `eu_reverse_charge_test.go` — German B2B buyer with `eu_vat`, 0%
  VAT line. Confirms NAV accepts the bare zero-rate shape today
  (the `vatOutOfScope` block is still a roadmap item).
- `mixed_vat_rates_test.go` — two lines at 27% + 5%. Exercises the
  per-rate `summaryByVatRate` bucketing and cross-bucket
  reconciliation against the invoice totals.
- `foreign_currency_test.go` — USD invoice with a stub
  `ExchangeRateProvider`. Validates the foreign-currency + HUF
  summary shape.
- `invoice_voided_test.go` — finalize → wait for NAV accept → void →
  STORNO with the worker's parent-dependency tracking.

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

## Contributing

PRs welcome — open one against `main`. A few conventions to save you a
review round-trip:

- **Conventional Commits.** Subject in imperative mood, ≤72 chars, no
  trailing period. Use a scope when it sharpens meaning
  (`fix(mapping):`, `test(e2e):`, `docs(readme):`). One focused logical
  change per commit — don't bundle unrelated edits, don't fragment one
  change across several commits.
- **Tests are required for new behaviour.** Mapping changes get unit
  tests against representative Stripe payloads; worker changes get
  store-driven tests with the in-memory store; NAV-client changes get
  a captured-XML or HTTP-recorder test. If a bug fix doesn't have a
  test pinning the fixed behaviour, the next change will silently
  re-break it.
- **Run the suite locally:** `task test:race` (race-enabled unit tests)
  before pushing. E2E tests are optional locally — they need real NAV
  test-environment credentials in `e2e/.env`. CI runs them on every PR.
- **Money math uses `math/big.Rat`.** No `float64` anywhere in the
  amount path. New code that touches monetary values must preserve
  this: rationals all the way to the final string-render boundary.
- **`net + vat = gross` must reconcile** in every emitted level (line,
  per-rate summary, invoice summary). Render net and vat first, then
  derive gross from the rounded values — never round the original
  sum independently. `TestMapInvoice_NetVatGrossReconciles` pins the
  contract.
- **Schema changes preserve NAV XSD field order.** Go's `encoding/xml`
  emits struct fields in declaration order; NAV validates field order
  strictly. New `InvoiceDetail`/`Line`/`Summary` fields must slot into
  the position they hold in the published XSD.
- **Keep the README in sync.** If your change touches anything the
  README documents (public API, config fields, store interface,
  Stripe events handled, env vars, test commands, package layout, any
  behaviour the README claims), update it in the same PR — or, if the
  code must stay strictly isolated, in an adjacent `docs(readme):`
  commit. A stale README is worse than no README.
- **Don't add features beyond what the change requires.** Bug fixes
  don't need surrounding refactors. One-shot helpers don't need
  abstractions. Three similar lines beats a premature abstraction.

## Roadmap

Rough order of upcoming work, surfaced from the known limitations
above. Pull requests covering any of these are welcome.

**Correctness gaps (high priority):**

1. **Fix `credit_note.*` handling.** The whole credit-note path needs
   work — see "Reliable credit-note handling" above. Concretely:
   - Split `credit_note.voided` into its own processor that emits a
     positive-amount reversing MODIFY instead of another negative
     MODIFY.
   - Copy `tax_behavior` in `creditNoteAsInvoice` so inclusive-tax
     credit notes don't ship the wrong amounts and rate.
   - Compute `modificationIndex` from prior submissions for the same
     invoice via `FindByInvoiceNumber`.
   - Distinguish `pre_payment` vs `post_payment` credit notes for
     §77 timing.
   - Add unit tests for each case (none currently exist).
2. **§58 subsequent-billing (utólagos) support.** Needed before
   enabling Stripe metered billing (`usage_type=metered`) or any
   arrears-billed subscription. Adds the +60-day clamp logic to the
   tax-point computation.
3. **VAT-status surfacing for non-standard supplies.** Map Stripe Tax's
   `taxability_reason` onto NAV's `vatExemption` / `vatOutOfScope`
   blocks for reverse-charge B2B EU and non-EU sales. Currently those
   collapse to `vatPercentage=0`, which is technically allowed but
   loses information NAV expects.

**Ergonomics (medium priority):**

4. **`paymentMethod` auto-derivation** from
   `invoice.collection_method` and
   `invoice.charge.payment_method_details` so mixed card / bank-
   transfer flows don't need a manual override per invoice.
5. **HU `eu_vat` → DOMESTIC fallback.** When a Hungarian buyer
   provides only `eu_vat HU12345678` (8 digits, no composite), the
   mapper currently classifies them as OTHER. Investigate whether a
   NAV taxpayer query can backfill the missing `vatCode`/`countyCode`
   without forcing the integrator to collect `hu_tin` separately.

**Audit defense (low priority):**

6. **`electronicInvoiceHash` over the Stripe PDF.** Optional under the
   current `completenessIndicator=false` mode, but adding it gives a
   stronger audit trail under Hungarian retention rules.

## License

MIT — see [LICENSE](LICENSE).
