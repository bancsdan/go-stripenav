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
- `mapping` package: pure Stripe `Invoice` → NAV `InvoiceData` translation,
  Hungarian VAT-number splitting, customer-category classification
  (DOMESTIC / OTHER / PRIVATE_PERSON), big.Rat-based monetary aggregation
  with HUF summary fallback, and typed `*MappingError` results.
- `nav` package: client for NAV Online Számla v3.0 covering
  `tokenExchange` (with AES-128-ECB token decryption + 30s-buffer cache),
  `manageInvoice`, `manageAnnulment`, `queryTransactionStatus`. Includes
  SHA-512 password hash and SHA3-512 request signing, typed `*NAVError`
  responses with retriability classification, and `MaxBatchSize` guard.
- `Submission` state machine (pending → submitted → processing →
  accepted / rejected / aborted), `SubmissionStore` interface,
  `InMemoryStore` reference implementation.
- `Worker` driving retries with exponential backoff (±20% jitter) and a
  hard 24-hour NAV reporting deadline; pluggable `MetricsRecorder` and
  `Clock` hooks.
- `examples/nethttp-server` showing minimal integration with
  environment-variable configuration and graceful shutdown.
- Test coverage: ≈81% (`mapping`), ≈69% (`nav`), ≈69%
  (root `stripenav`); race-clean under `go test ./... -race`.
