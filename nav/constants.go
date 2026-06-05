package nav

// Well-known base URLs for the NAV Online Számla API.
const (
	ProductionBaseURL = "https://api.onlineszamla.nav.gov.hu/invoiceService/v3"
	TestBaseURL       = "https://api-test.onlineszamla.nav.gov.hu/invoiceService/v3"
)

// MaxBatchSize is the documented per-batch operation limit for both
// manageInvoice and manageAnnulment.
const MaxBatchSize = 100

// DefaultRateLimit is the per-client outbound request ceiling, in
// requests per second. NAV throttles a technical user that exceeds
// roughly one request per second across all endpoints; 1.0 mirrors
// that expectation.
const DefaultRateLimit = 1.0

// DefaultRateBurst is the bucket size for the rate limiter. A burst of
// 1 means strictly even spacing.
const DefaultRateBurst = 1
