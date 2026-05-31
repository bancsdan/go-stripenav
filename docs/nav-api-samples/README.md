# NAV Online Számla v3.0 — sample requests

These XML files are the request samples NAV publishes alongside the v3.0
interface specification. They live here as reference material for
developers cross-checking the wire format produced by the `nav` package
or debugging differences between our envelope and NAV's expectations.

| File | Endpoint |
| --- | --- |
| `tokenExchange.xml` | `POST /tokenExchange` |
| `manageInvoice.xml` | `POST /manageInvoice` |
| `manageAnnulment.xml` | `POST /manageAnnulment` |
| `queryTransactionStatus.xml` | `POST /queryTransactionStatus` |
| `queryTransactionList.xml` | `POST /queryTransactionList` |
| `queryInvoiceData.xml` | `POST /queryInvoiceData` |
| `queryInvoiceCheck.xml` | `POST /queryInvoiceCheck` |
| `queryInvoiceChainDigest.xml` | `POST /queryInvoiceChainDigest` |
| `queryInvoiceDigest_outbound_query_params.xml` | `POST /queryInvoiceDigest` (outbound query) |
| `queryInvoiceDigest_inbound_invoice_chain.xml` | `POST /queryInvoiceDigest` (inbound chain) |
| `queryTaxpayer.xml` | `POST /queryTaxpayer` |

Origin: NAV (Hungarian National Tax and Customs Administration). The
files are unmodified copies of the published v3.0 reference samples.

These are not loaded by any test at runtime; they are documentation. If
you later add tests that parse them to verify round-trip behaviour
against the hand-written schema structs in `nav/schemas/`, the
idiomatic move is to copy the file(s) you need into `nav/testdata/`,
which the Go toolchain explicitly excludes from build / vet / lint.
