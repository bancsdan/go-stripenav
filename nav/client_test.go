package nav

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:     baseURL,
		Login:       "lwilsmn0uqdxe6u",
		Password:    "secret",
		TaxNumber:   "11111111",
		SignKey:     "ac-ac3a-7f661bff7d342N43CYX4U9FG",
		ExchangeKey: "0123456789ABCDEF",
		Software: Software{
			ID:             "SW01",
			Name:           "go-stripenav",
			Operation:      "LOCAL_SOFTWARE",
			MainVersion:    "0.1.0",
			DevName:        "test",
			DevContact:     "test@example.com",
			DevCountryCode: "HU",
		},
		Clock: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
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
		mut  func(*Config)
		want string
	}{
		{"missing base", func(c *Config) { c.BaseURL = "" }, "BaseURL"},
		{"missing login", func(c *Config) { c.Login = "" }, "Login"},
		{"missing password", func(c *Config) { c.Password = "" }, "Password"},
		{"missing tax", func(c *Config) { c.TaxNumber = "" }, "TaxNumber"},
		{"short tax", func(c *Config) { c.TaxNumber = "1234567" }, "does not contain 8 digits"},
		{"non-digit tax", func(c *Config) { c.TaxNumber = "abc-def-ghi" }, "does not contain 8 digits"},
		{"missing signKey", func(c *Config) { c.SignKey = "" }, "SignKey"},
		{"bad exchangeKey", func(c *Config) { c.ExchangeKey = "shortkey" }, "ExchangeKey"},
		{"missing software", func(c *Config) { c.Software.ID = "" }, "Software"},
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
	_, err = c.SubmitInvoice(ctx, []InvoiceOperation{{Operation: "CREATE", InvoiceData: []byte("<x/>")}})
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
	ops := make([]InvoiceOperation, MaxBatchSize+1)
	for i := range ops {
		ops[i] = InvoiceOperation{Operation: "CREATE", InvoiceData: []byte("<x/>")}
	}
	_, err = c.SubmitInvoice(context.Background(), ops)
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("expected ErrBatchTooLarge, got %v", err)
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
	var navErr *NAVError
	if !errors.As(err, &navErr) {
		t.Fatalf("expected *NAVError, got %T %v", err, err)
	}
	if navErr.HTTPStatus != 500 || navErr.Code != "INTERNAL_ERROR" || !navErr.Retriable {
		t.Fatalf("unexpected NAVError: %+v", navErr)
	}
}
