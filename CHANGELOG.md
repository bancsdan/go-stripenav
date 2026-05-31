# Changelog

All notable changes to this project are documented in this file.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
