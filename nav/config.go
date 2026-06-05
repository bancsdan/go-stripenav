package nav

import (
	"net/http"
	"time"
)

// Config carries the credentials and runtime knobs the NAV client needs.
// Pass this in via stripenav.Config.NAV; the bridge handler constructs
// the actual transport client from it internally.
type Config struct {
	// BaseURL points at either ProductionBaseURL or TestBaseURL. Must be
	// set explicitly so a misconfigured caller never targets production.
	BaseURL string

	// Login is the NAV technical user login (max 15 chars).
	Login string
	// Password is the NAV technical user password (plain — the client
	// hashes it before sending).
	Password string
	// TaxNumber is the 8-digit Hungarian tax number the technical user
	// belongs to.
	TaxNumber string
	// SignKey is the technical user's signature key (32 chars) used in
	// the SHA3-512 request signature.
	SignKey string
	// ExchangeKey is the technical user's exchange key (16 chars) used to
	// AES-128-ECB-decrypt the encodedExchangeToken returned by NAV.
	ExchangeKey string

	// Software identifies the calling application.
	Software Software

	// HTTPClient is used for all outbound NAV requests. Defaults to a new
	// http.Client with a 30-second timeout. Wrap the transport here for
	// tracing, metrics, or distributed rate limiting.
	HTTPClient *http.Client

	// Clock returns "now" for header timestamps. Defaults to time.Now.
	Clock func() time.Time

	// Debug, when true, logs every outbound NAV request and inbound
	// response body via slog.Default(). Use only locally — bodies
	// include the signed envelope and (decoded) responses.
	Debug bool

	// RateLimit caps outbound requests to NAV at this many per second.
	// NAV throttles clients that exceed roughly one request per second;
	// the default (DefaultRateLimit = 1.0) matches that. Set higher if
	// NAV later raises the ceiling for your account. Ignored when
	// DisableRateLimit=true.
	RateLimit float64

	// RateBurst is the maximum burst the limiter allows before
	// re-spacing. Defaults to DefaultRateBurst (1) — strictly even
	// spacing.
	RateBurst int

	// DisableRateLimit skips outbound rate limiting entirely. Use in
	// unit tests where wall-clock waits are noise, or against a gateway
	// that handles rate limiting upstream.
	DisableRateLimit bool
}
