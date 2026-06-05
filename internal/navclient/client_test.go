package navclient

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancsdan/go-stripenav/nav"
)

func testConfig(baseURL string) nav.Config {
	return nav.Config{
		BaseURL:     baseURL,
		Login:       "lwilsmn0uqdxe6u",
		Password:    "secret",
		TaxNumber:   "11111111",
		SignKey:     "ac-ac3a-7f661bff7d342N43CYX4U9FG",
		ExchangeKey: "0123456789ABCDEF",
		Software: nav.Software{
			ID:             "SW01",
			Name:           "go-stripenav",
			Operation:      "LOCAL_SOFTWARE",
			MainVersion:    "0.1.0",
			DevName:        "test",
			DevContact:     "test@example.com",
			DevCountryCode: "HU",
		},
		Clock: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
		// Disable rate limiting in unit tests — the default 1/s
		// would add seconds of wall time to multi-call tests for no
		// gain (we're not testing rate-limit behaviour here).
		// TestClient_RateLimit explicitly enables it.
		DisableRateLimit: true,
	}
}

func TestNewClient_NormalisesTaxNumber(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"12345678", "12345678"},
		{"12345678901", "12345678"},
		{"12345678-9-01", "12345678"},
		{"HU12345678901", "12345678"},
		{" 12345678-9-01 ", "12345678"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			cfg := testConfig("https://example/v3")
			cfg.TaxNumber = c.in
			cli, err := NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if cli.cfg.TaxNumber != c.want {
				t.Fatalf("TaxNumber = %q, want %q", cli.cfg.TaxNumber, c.want)
			}
		})
	}
}

func TestNewClient_Validation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*nav.Config)
		want string
	}{
		{"missing base", func(c *nav.Config) { c.BaseURL = "" }, "BaseURL"},
		{"missing login", func(c *nav.Config) { c.Login = "" }, "Login"},
		{"missing password", func(c *nav.Config) { c.Password = "" }, "Password"},
		{"missing tax", func(c *nav.Config) { c.TaxNumber = "" }, "TaxNumber"},
		{"short tax", func(c *nav.Config) { c.TaxNumber = "1234567" }, "does not contain 8 digits"},
		{"non-digit tax", func(c *nav.Config) { c.TaxNumber = "abc-def-ghi" }, "does not contain 8 digits"},
		{"missing signKey", func(c *nav.Config) { c.SignKey = "" }, "SignKey"},
		{"bad exchangeKey", func(c *nav.Config) { c.ExchangeKey = "shortkey" }, "ExchangeKey"},
		{"missing software", func(c *nav.Config) { c.Software.ID = "" }, "Software"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig("https://example/v3")
			tc.mut(&cfg)
			_, err := NewClient(cfg)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestClient_ExchangeToken_CachesAndDecrypts(t *testing.T) {
	cfg := testConfig("")

	// Pre-compute the PKCS#7-padded, AES-128-ECB-encrypted, base64-encoded
	// token NAV would return on the wire.
	plain := "74ec2947-23a0-4730-b428-62f8d1f8e0ca5EE6BOUQ8Q21"
	encoded := encryptForTest(t, []byte(plain), []byte(cfg.ExchangeKey))

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/tokenExchange" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		validTo := cfg.Clock().Add(5 * time.Minute).Format("2006-01-02T15:04:05.000Z")
		validFrom := cfg.Clock().Format("2006-01-02T15:04:05.000Z")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, xml.Header+`<TokenExchangeResponse>`+
			`<header><requestId>R1</requestId><timestamp>`+validFrom+`</timestamp><requestVersion>3.0</requestVersion><headerVersion>1.0</headerVersion></header>`+
			`<result><funcCode>OK</funcCode></result>`+
			`<encodedExchangeToken>`+encoded+`</encodedExchangeToken>`+
			`<tokenValidityFrom>`+validFrom+`</tokenValidityFrom>`+
			`<tokenValidityTo>`+validTo+`</tokenValidityTo>`+
			`</TokenExchangeResponse>`)
	}))
	defer srv.Close()

	cfg.BaseURL = srv.URL
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tok, err := c.ExchangeToken(context.Background())
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if tok.Value != plain {
		t.Fatalf("token value mismatch: got %q want %q", tok.Value, plain)
	}

	// Second call must NOT hit a cache — NAV exchange tokens are
	// single-use and reusing them yields INVALID_EXCHANGE_TOKEN.
	_, err = c.ExchangeToken(context.Background())
	if err != nil {
		t.Fatalf("ExchangeToken (second): %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 server hits (no caching), got %d", got)
	}
}

func TestClient_QueryTransactionStatus_ParsesValidationMessages(t *testing.T) {
	cfg := testConfig("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, xml.Header+`<QueryTransactionStatusResponse>`+
			`<header><requestId>R1</requestId><timestamp>x</timestamp><requestVersion>3.0</requestVersion><headerVersion>1.0</headerVersion></header>`+
			`<result><funcCode>OK</funcCode></result>`+
			`<processingResults>`+
			`<processingResult>`+
			`<index>1</index>`+
			`<invoiceStatus>SAVED</invoiceStatus>`+
			`<technicalValidationMessages><validationResultCode>WARN</validationResultCode><validationErrorCode>SCHEMA_LOOSE</validationErrorCode><message>loose</message></technicalValidationMessages>`+
			`<businessValidationMessages><validationResultCode>INFO</validationResultCode><validationErrorCode>INFO_CODE</validationErrorCode><message>ok</message></businessValidationMessages>`+
			`<compressedContentIndicator>false</compressedContentIndicator>`+
			`</processingResult>`+
			`<originalRequestVersion>3.0</originalRequestVersion>`+
			`</processingResults>`+
			`</QueryTransactionStatusResponse>`)
	}))
	defer srv.Close()

	cfg.BaseURL = srv.URL
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.QueryTransactionStatus(context.Background(), "T1", false)
	if err != nil {
		t.Fatalf("QueryTransactionStatus: %v", err)
	}
	if len(resp.ProcessingResults.ProcessingResult) != 1 {
		t.Fatalf("expected 1 processing result, got %d", len(resp.ProcessingResults.ProcessingResult))
	}
	pr := resp.ProcessingResults.ProcessingResult[0]
	if pr.InvoiceStatus != "SAVED" || len(pr.TechnicalValidationMessages) != 1 || len(pr.BusinessValidationMessages) != 1 {
		t.Fatalf("processing result missing fields: %+v", pr)
	}
}

func TestClient_SubmitInvoice_ContextCancellation(t *testing.T) {
	cfg := testConfig("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg.BaseURL = srv.URL

	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.SubmitInvoice(ctx, []nav.InvoiceOperation{{Operation: "CREATE", InvoiceData: []byte("<x/>")}})
	if err == nil {
		t.Fatalf("expected context error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

func TestClient_SubmitInvoice_BatchTooLarge(t *testing.T) {
	cfg := testConfig("https://example/v3")
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ops := make([]nav.InvoiceOperation, nav.MaxBatchSize+1)
	for i := range ops {
		ops[i] = nav.InvoiceOperation{Operation: "CREATE", InvoiceData: []byte("<x/>")}
	}
	_, err = c.SubmitInvoice(context.Background(), ops)
	if !errors.Is(err, nav.ErrBatchTooLarge) {
		t.Fatalf("expected nav.ErrBatchTooLarge, got %v", err)
	}
}

// TestClient_RateLimitSpacesRequests pins the per-client rate limiter
// behaviour: with a 10/s rate and the default burst of 1, three
// successive `do` calls must be spaced ~100ms apart. The first call
// consumes the burst and goes through immediately; calls 2 and 3 each
// wait one bucket interval. This catches regressions where the
// limiter is wired up but the Wait() is bypassed or where the
// configured rate isn't honoured.
func TestClient_RateLimitSpacesRequests(t *testing.T) {
	var (
		mu        sync.Mutex
		timestamps []time.Time
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		timestamps = append(timestamps, time.Now())
		mu.Unlock()
		// Return whatever; we're only checking timing here.
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, xml.Header+`<TokenExchangeResponse xmlns="http://schemas.nav.gov.hu/OSA/3.0/api"><result xmlns="http://schemas.nav.gov.hu/NTCA/1.0/common"><funcCode>OK</funcCode></result><encodedExchangeToken>AA==</encodedExchangeToken><tokenValidityFrom>2026-01-01T12:00:00.000Z</tokenValidityFrom><tokenValidityTo>2026-01-01T12:05:00.000Z</tokenValidityTo></TokenExchangeResponse>`)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.DisableRateLimit = false
	cfg.RateLimit = 10 // 100ms between calls
	cfg.RateBurst = 1
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	for i := 0; i < 3; i++ {
		// ExchangeToken's parse will fail (the fixture token is too
		// short to decrypt), but the HTTP call still completes — that's
		// all we need for the timestamp check.
		_, _ = c.ExchangeToken(context.Background())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(timestamps) != 3 {
		t.Fatalf("got %d server hits, want 3", len(timestamps))
	}
	// Two gaps, each should be ≥ ~100ms (the bucket refill interval).
	// Allow a small slack — the limiter is precise but goroutine
	// scheduling can shave a few ms.
	for i := 1; i < len(timestamps); i++ {
		gap := timestamps[i].Sub(timestamps[i-1])
		if gap < 80*time.Millisecond {
			t.Errorf("gap %d-%d = %s, want ≥80ms (10/s limiter)",
				i-1, i, gap)
		}
	}
}

func TestClient_DoSurfacesNAVError(t *testing.T) {
	cfg := testConfig("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, xml.Header+`<GeneralExceptionResponse><funcCode>ERROR</funcCode><errorCode>INTERNAL_ERROR</errorCode><message>boom</message></GeneralExceptionResponse>`)
	}))
	defer srv.Close()
	cfg.BaseURL = srv.URL
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.ExchangeToken(context.Background())
	var navErr *nav.NAVError
	if !errors.As(err, &navErr) {
		t.Fatalf("expected *nav.NAVError, got %T %v", err, err)
	}
	if navErr.HTTPStatus != 500 || navErr.Code != "INTERNAL_ERROR" || !navErr.Retriable {
		t.Fatalf("unexpected nav.NAVError: %+v", navErr)
	}
}
