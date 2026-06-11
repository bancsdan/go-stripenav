# Changelog

All notable changes to this project are documented in this file.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Fixed

- Retriable `queryTransactionStatus` failures now back off in place
  (keeping the NAV `transactionId`) instead of silently failing a
  `submitted → pending` transition and hot-looping the poll until the
  24-hour deadline.
- `invoiceAsStorno` propagates `tax_behavior`, so stornos of
  inclusive-tax (B2C) invoices report correct net/VAT amounts.
- The handler returns 503 for transient failures (store unavailable,
  exchange-rate lookup failed) so Stripe redelivers, and 200 only for
  permanent ones; previously every failure was ACKed and transient
  failures lost the event.
- `MapInvoice` rejects invoices whose embedded Stripe line list is
  truncated (`lines.has_more`) instead of silently under-reporting.
- Per-rate VAT summary buckets key on the rendered 2-decimal
  `vatPercentage`, so minor-unit rounding drift can no longer split one
  legal rate into duplicate `summaryByVatRate` rows.
- Customer tax-id classification scans by specificity (HU > EU > third
  state) instead of list order.
- `PRIVATE_PERSON` customer blocks omit name/address per NAV's
  data-minimisation rule; customer addresses without street detail are
  omitted entirely (NAV's `simpleAddress` requires a non-blank
  `additionalAddressDetail`); incomplete supplier addresses fail
  mapping with `SUPPLIER_ADDRESS_REQUIRED`.
- A partially finished multi-operation NAV transaction is no longer
  reported as accepted (`overallStatus` requires every operation final).
- Storno line `lineNumberReference` chain positions derive from the
  original invoice's line count (`MapOptions.OriginalLineCount`).
- Exchange rates resolve at the invoice's issue time instead of
  processing time.

### Changed

- Submissions past the 24-hour NAV reporting deadline are no longer
  aborted: late reporting is still legally required (and beats never
  reporting), so the worker keeps retrying and instead emits a Warn log
  plus a `deadline_exceeded` metric event. `StatusAborted` now only
  arises from the parent-failed path.
- `SubmissionStore.Put` should wrap the new `stripenav.ErrAlreadyExists`
  for duplicate event ids; the bundled in-memory store does.
- `WorkerConfig.Supplier` removed (was never read).
- Worker `UpdateStatus`/`ReleaseClaim` failures are logged instead of
  silently discarded.
- `.golangci.yml` migrated to golangci-lint v2; CI runs the linter.

### Added

- `stripenav.Handler` — the embeddable `http.Handler` Hungarian Stripe
  merchants mount on their backend and register as a Stripe webhook
  endpoint.
- `stripenav.Config` — single struct for Stripe-side, NAV-side, supplier,
  store, exchange-rate, logging, metrics, and clock configuration.
- Stripe `Invoice` → NAV `InvoiceData` translation (in
  `internal/invoicemap`): Hungarian VAT-number splitting,
  customer-category classification (DOMESTIC / OTHER / PRIVATE_PERSON),
  big.Rat-based monetary aggregation with HUF summary rendering, and
  typed `*MappingError` results. The consumer-facing `Supplier` and
  `Address` types live in the public `mapping` package.
- NAV Online Számla v3.0 client (in `internal/navclient`, configured
  via the public `nav.Config`) covering `tokenExchange` (AES-128-ECB
  token decryption; tokens are single-use, deliberately uncached),
  `manageInvoice`, `manageAnnulment`, `queryTransactionStatus`.
  Includes SHA-512 password hash and SHA3-512 request signing, typed
  `*nav.NAVError` responses with retriability classification,
  `MaxBatchSize` guard, and a 1 req/s token-bucket rate limiter
  matching NAV's per-source-IP ceiling.
- `Submission` state machine (pending → submitted → processing →
  accepted / rejected / aborted), `SubmissionStore` interface, and an
  internal in-memory reference store used automatically when
  `Config.Store` is nil (dev/test only — state is lost on restart).
- `Worker` driving retries with exponential backoff (±20% jitter),
  claim/lease-based multi-replica safety, parent-dependency tracking
  for stornos, and a 24-hour reporting-deadline alarm; pluggable
  `MetricsRecorder` and `Clock` hooks.
- `docs/EMBED.md` integration guide; e2e suite against the real NAV
  test environment (`-tags=navtest`); race-clean under
  `go test ./... -race`.
