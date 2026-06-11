# Changelog

## [0.4.0](https://github.com/bancsdan/go-stripenav/compare/v0.3.0...v0.4.0) (2026-06-11)


### Added

* **mapping:** support §58 periodic settlement for subscriptions ([356a6f0](https://github.com/bancsdan/go-stripenav/commit/356a6f0378e0e0fe8edb53853a6fc5c5b1fc3e79))
* **nav:** rate-limit outbound requests to 1/s by default ([8671ae1](https://github.com/bancsdan/go-stripenav/commit/8671ae10ec679df01175d16709e6569d482a4e7c))


### Fixed

* address review findings across worker, mapping, and credit-note paths ([bc97ca0](https://github.com/bancsdan/go-stripenav/commit/bc97ca0d27a5978287ae137335d3983f89424a42))
* close storno/credit-note gaps the truncation guard and FX anchoring left open ([29a5f41](https://github.com/bancsdan/go-stripenav/commit/29a5f417bda549451eca862309401d90268bce7f))
* **credit_note:** give STORNO its own issue date ([f89ae81](https://github.com/bancsdan/go-stripenav/commit/f89ae81a3b0b465f20f21fbfa55221d232622995))
* **mapping:** handle Stripe Tax inclusive pricing and reconcile rounding ([a057fb9](https://github.com/bancsdan/go-stripenav/commit/a057fb98de6c1bdc4ef7d4258b4a8498e205ab60))
* **mapping:** render NAV date fields in Hungarian calendar (UTC+2) ([f8630f8](https://github.com/bancsdan/go-stripenav/commit/f8630f81984cb871989ee037e05e031a24e08a99))
* **navclient:** use stdlib crypto/sha3 instead of golang.org/x/crypto/sha3 ([1850aa8](https://github.com/bancsdan/go-stripenav/commit/1850aa8cce631bca7bfb478dc82204ad552d165b))
* resolve full code-review findings — poll requeue, ACK semantics, mapping edge cases ([1851a0f](https://github.com/bancsdan/go-stripenav/commit/1851a0fc788f5492f4af03aa1b2f88270d7e0730))
* **storeinmem:** don't re-claim rows we already hold ([e442662](https://github.com/bancsdan/go-stripenav/commit/e44266227a1fcbb4aba542e7470028903bdae409))
* **worker:** cancel lifecycle when lease renewal fails ([6730270](https://github.com/bancsdan/go-stripenav/commit/6730270554efe3eac6a711d24aad2a4a08971a04))
* **worker:** don't warn on context cancellation during shutdown ([34dc71b](https://github.com/bancsdan/go-stripenav/commit/34dc71b8ee4143e74b38a7bc54a0794e6db04f4f))
* **worker:** keep retrying past the 24h reporting deadline instead of aborting ([409cc04](https://github.com/bancsdan/go-stripenav/commit/409cc04a04494c0877735b86b9a42ddfbabd6078))
* **worker:** surface NAV validation messages on ABORTED ([b15089e](https://github.com/bancsdan/go-stripenav/commit/b15089ec1b2e1b0fa6ffc4d1bb98c03febdfaa74))
* **worker:** wait for lease-renew goroutine before releasing claim ([88c2be6](https://github.com/bancsdan/go-stripenav/commit/88c2be68a5146fb250a4428aabb95ec2bd4b3da4))


### Documentation

* bring EMBED.md and CHANGELOG in line with the current API ([9a9e114](https://github.com/bancsdan/go-stripenav/commit/9a9e114686946dac3df9cd42bee70c8f6d415e55))
* **readme:** add Contributing and Roadmap sections ([9d51808](https://github.com/bancsdan/go-stripenav/commit/9d51808a884649db634ebfcb38d7003f0d32bdfe))
* **readme:** explain NAV's per-IP rate limit for multi-replica setups ([b0b0115](https://github.com/bancsdan/go-stripenav/commit/b0b011597aa0c84a7a3a5edfc85c122220b05b64))
* **readme:** fix inconsistencies and make the multi-replica section cloud-neutral ([410b5bc](https://github.com/bancsdan/go-stripenav/commit/410b5bce29a8c596519975ce840cbc59bd04929a))
* **readme:** flag credit_note.* handling as broken ([b281f01](https://github.com/bancsdan/go-stripenav/commit/b281f01a4da381e4d37d5da1b22597c1ed1a2ab9))
* **readme:** refresh for claim-based store, worker pacing, e2e harness ([c2fcee3](https://github.com/bancsdan/go-stripenav/commit/c2fcee368f744144cc32d1d96566456914804956))


### Changed

* default Config.Store to internal in-memory store; hide storeinmem ([f593dbe](https://github.com/bancsdan/go-stripenav/commit/f593dbede24baf43726f1014e6a4080361b5afc8))
* hide NAV client and mapping internals behind internal/ ([c89ad34](https://github.com/bancsdan/go-stripenav/commit/c89ad34f4f503d44bfe01b8919e7a26e4cca9c5d))
* **mapping:** detect §58 periodicity via period span ([2f82f62](https://github.com/bancsdan/go-stripenav/commit/2f82f625afc5f24501d4698c37abce8785fa2c70))

## Changelog

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
