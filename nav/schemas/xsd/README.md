# NAV Online Számla v3.0 XSDs

This directory is the source of truth for `task generate`, which produces
the `nav/schemas/generated/` reference structs. The hand-written
structs in `nav/schemas/*.go` are the package's runtime types; the
generated structs exist purely so we can diff against them and spot
fields we've missed or got wrong.

## Why the XSDs aren't checked in

NAV does not publish the XSDs at a stable anonymous URL. They ship as
part of the developer ZIP downloaded from the NAV Online Számla portal
(account required, terms of use apply per file). For that reason we
expect each user to drop their licensed copy here locally.

## Getting the XSDs

1. Log in to <https://onlineszamla.nav.gov.hu/> (or the test portal at
   <https://onlineszamla-test.nav.gov.hu/>) using your taxpayer or
   technical-user credentials.
2. Navigate to **Információk → Letöltések** (Information → Downloads).
3. Download the "Szolgáltatás specifikáció v3.0" ZIP. It contains the
   five XSDs listed below in a `Schemas/` directory.
4. Copy them into this directory (without renaming):

```
nav/schemas/xsd/
├── common.xsd            # NTCA/1.0/common
├── invoiceBase.xsd       # OSA/3.0/base
├── invoiceApi.xsd        # OSA/3.0/api
├── invoiceData.xsd       # OSA/3.0/data
└── invoiceAnnulment.xsd  # OSA/3.0/annul
```

## Generating structs

With the XSDs in place and [xgen](https://github.com/xuri/xgen) on PATH:

```sh
task generate
```

Output lands in `nav/schemas/generated/`. That package is **not used at
runtime**; it exists so we can validate the hand-written structs by:

- Eyeballing field lists for additions we missed (current incident
  history: `lineModificationReference`, `lineNumberReference`,
  `modificationIndex` ≥ 1).
- Writing a small diff harness (TODO) that round-trips a sample
  `InvoiceData` through both struct sets and reports field-level deltas.

## Why we don't ship the generated structs as the runtime types

The hand-written structs in `nav/schemas/*.go` have been hardened by
multiple rounds of NAV business validation feedback (real test-env
ABORTED responses, then a fix landing the missing field). Swapping in
generated structs mid-flight would risk losing that earned correctness
to subtle differences in field ordering, naming, or namespace
qualifications. A planned `v0.3` task: write the comparison harness,
verify the generated structs match the hand-written ones byte-for-byte
on the sample fixtures in `docs/nav-api-samples/`, then swap.
