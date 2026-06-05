// Package nav holds the public NAV-side types the bridge consumer
// configures: Config (credentials and runtime knobs), Software, the
// per-batch operation types (InvoiceOperation, AnnulmentOperation),
// SubmitResult, NAVError, and the base URL / rate limit constants.
// The actual HTTP client implementation lives in internal/navclient
// and is constructed by stripenav.Handler from the Config the caller
// supplies.
package nav
