package nav

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bancsdan/go-stripenav/nav/schemas"
	"golang.org/x/time/rate"
)

// DefaultRateLimit is the per-client outbound request ceiling, in
// requests per second. NAV throttles a technical user that exceeds
// roughly one request per second across all endpoints; 1.0 mirrors
// that expectation. Override via Config.RateLimit.
const DefaultRateLimit = 1.0

// DefaultRateBurst is the bucket size for the rate limiter. A burst
// of 1 means strictly even spacing. Override via Config.RateBurst.
const DefaultRateBurst = 1

// Well-known base URLs for the NAV Online Számla API.
const (
	ProductionBaseURL = "https://api.onlineszamla.nav.gov.hu/invoiceService/v3"
	TestBaseURL       = "https://api-test.onlineszamla.nav.gov.hu/invoiceService/v3"
)

// MaxBatchSize is the documented per-batch operation limit for both
// manageInvoice and manageAnnulment.
const MaxBatchSize = 100

// TODO: refactor with cleaner config handling with default values and validation, etc
// Config carries the credentials and runtime knobs required by Client.
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
	// http.Client with a 30-second timeout.
	HTTPClient *http.Client

	// Clock returns "now" for header timestamps. Defaults to time.Now.
	Clock func() time.Time

	// Debug, when true, logs every outbound NAV request and inbound
	// response body via slog.Default(). Use only locally — bodies
	// include the signed envelope and (decoded) responses.
	Debug bool

	// RateLimit caps outbound requests to NAV at this many per second.
	// NAV throttles clients that exceed roughly one request per second;
	// the default (DefaultRateLimit = 1.0) matches that. Set to a
	// higher value if NAV later raises the ceiling for your account.
	// Ignored when DisableRateLimit=true.
	RateLimit float64

	// RateBurst is the maximum burst the limiter allows before
	// re-spacing. Defaults to DefaultRateBurst (1) — strictly even
	// spacing. A larger value lets you absorb a small spike before
	// settling back to the steady rate.
	RateBurst int

	// DisableRateLimit skips outbound rate limiting entirely. Use in
	// unit tests where wall-clock waits are noise, or against a
	// gateway that handles rate limiting upstream.
	DisableRateLimit bool
}

// Client is the NAV Online Számla v3.0 API client.
type Client struct {
	cfg     Config
	limiter *rate.Limiter // nil when DisableRateLimit=true
}

// Token is an exchange token returned by /tokenExchange.
type Token struct {
	Value     string // AES-decrypted plaintext token (length 22 in practice)
	ValidFrom time.Time
	ExpiresAt time.Time
}

// NewClient returns a configured *Client or an error if Config is missing
// required fields.
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("nav: BaseURL is required (use ProductionBaseURL or TestBaseURL)")
	}
	if cfg.Login == "" {
		return nil, errors.New("nav: Login is required")
	}
	if cfg.Password == "" {
		return nil, errors.New("nav: Password is required")
	}
	if cfg.TaxNumber == "" {
		return nil, errors.New("nav: TaxNumber is required")
	}
	// NAV's <common:taxNumber> requires exactly 8 digits (the taxpayer
	// "törzsszám"). Accept the full 11-char composite (12345678-9-01,
	// 12345678901, HU12345678901) and normalise to the first 8 digits.
	taxDigits := stripNonDigits(cfg.TaxNumber)
	if len(taxDigits) < 8 {
		return nil, fmt.Errorf("nav: TaxNumber %q does not contain 8 digits", cfg.TaxNumber)
	}
	cfg.TaxNumber = taxDigits[:8]
	if cfg.SignKey == "" {
		return nil, errors.New("nav: SignKey is required")
	}
	if len(cfg.ExchangeKey) != 16 {
		return nil, fmt.Errorf("nav: ExchangeKey must be exactly 16 bytes (AES-128), got %d", len(cfg.ExchangeKey))
	}
	if cfg.Software.ID == "" || cfg.Software.Name == "" || cfg.Software.Operation == "" {
		return nil, errors.New("nav: Software.ID, Software.Name and Software.Operation are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	c := &Client{cfg: cfg}
	if !cfg.DisableRateLimit {
		r := cfg.RateLimit
		if r <= 0 {
			r = DefaultRateLimit
		}
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = DefaultRateBurst
		}
		c.limiter = rate.NewLimiter(rate.Limit(r), burst)
	}
	return c, nil
}

func stripNonDigits(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// ExchangeToken fetches a fresh NAV exchange token. NAV tokens are
// single-use — each manageInvoice or manageAnnulment call consumes
// exactly one — so the client deliberately does not cache.
func (c *Client) ExchangeToken(ctx context.Context) (Token, error) {
	ec := newEnvelopeContext(c.cfg.Clock(), c.cfg.SignKey, nil)
	req := tokenExchangeRequest{
		XmlnsCom: xmlnsCommonURI,
		Header:   ec.header(),
		User:     ec.user(c.cfg.Login, c.cfg.Password, c.cfg.TaxNumber),
		Software: c.cfg.Software.toXML(),
	}

	var resp schemas.TokenExchangeResponse
	if err := c.do(ctx, "/tokenExchange", req, &resp); err != nil {
		return Token{}, err
	}

	plain, err := decryptExchangeToken(resp.EncodedExchangeToken, []byte(c.cfg.ExchangeKey))
	if err != nil {
		return Token{}, err
	}
	validFrom, _ := time.Parse(time.RFC3339Nano, resp.TokenValidityFrom)
	validTo, _ := time.Parse(time.RFC3339Nano, resp.TokenValidityTo)
	return Token{Value: plain, ValidFrom: validFrom, ExpiresAt: validTo}, nil
}

// InvoiceOperation is one operation in a manageInvoice batch.
type InvoiceOperation struct {
	// Operation is CREATE, MODIFY or STORNO.
	Operation string
	// InvoiceData is the marshalled, base64-unencoded XML for one
	// InvoiceData document. The client base64-encodes it before sending
	// and computes the per-operation hash over the encoded form.
	InvoiceData []byte
}

// SubmitResult is what the client returns for a successful submission.
type SubmitResult struct {
	TransactionID string
}

// SubmitInvoice submits a batch of CREATE/MODIFY/STORNO operations.
func (c *Client) SubmitInvoice(ctx context.Context, ops []InvoiceOperation) (SubmitResult, error) {
	if len(ops) == 0 {
		return SubmitResult{}, errors.New("nav: SubmitInvoice requires at least one operation")
	}
	if len(ops) > MaxBatchSize {
		return SubmitResult{}, ErrBatchTooLarge
	}
	tok, err := c.ExchangeToken(ctx)
	if err != nil {
		return SubmitResult{}, err
	}

	encOps := make([]invoiceOperationXML, len(ops))
	signedOps := make([]SignedOperation, len(ops))
	for i, op := range ops {
		encoded := base64.StdEncoding.EncodeToString(op.InvoiceData)
		encOps[i] = invoiceOperationXML{
			Index:            i + 1,
			InvoiceOperation: op.Operation,
			InvoiceData:      encoded,
		}
		signedOps[i] = SignedOperation{
			Operation:     op.Operation,
			Base64Payload: encoded,
		}
	}

	ec := newEnvelopeContext(c.cfg.Clock(), c.cfg.SignKey, signedOps)
	req := manageInvoiceRequest{
		XmlnsCom:      xmlnsCommonURI,
		Header:        ec.header(),
		User:          ec.user(c.cfg.Login, c.cfg.Password, c.cfg.TaxNumber),
		Software:      c.cfg.Software.toXML(),
		ExchangeToken: tok.Value,
		Operations: invoiceOperationsXML{
			CompressedContent: false,
			Operations:        encOps,
		},
	}

	var resp schemas.ManageInvoiceResponse
	if err := c.do(ctx, "/manageInvoice", req, &resp); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{TransactionID: resp.TransactionID}, nil
}

// AnnulmentOperation is one operation in a manageAnnulment batch.
type AnnulmentOperation struct {
	// InvoiceAnnulment is the marshalled, base64-unencoded XML for one
	// InvoiceAnnulment document.
	InvoiceAnnulment []byte
}

// AnnulInvoice submits one or more ANNUL operations.
func (c *Client) AnnulInvoice(ctx context.Context, ops []AnnulmentOperation) (SubmitResult, error) {
	if len(ops) == 0 {
		return SubmitResult{}, errors.New("nav: AnnulInvoice requires at least one operation")
	}
	if len(ops) > MaxBatchSize {
		return SubmitResult{}, ErrBatchTooLarge
	}
	tok, err := c.ExchangeToken(ctx)
	if err != nil {
		return SubmitResult{}, err
	}

	encOps := make([]annulmentOperationXML, len(ops))
	signedOps := make([]SignedOperation, len(ops))
	const annulOp = "ANNUL"
	for i, op := range ops {
		encoded := base64.StdEncoding.EncodeToString(op.InvoiceAnnulment)
		encOps[i] = annulmentOperationXML{
			Index:              i + 1,
			AnnulmentOperation: annulOp,
			InvoiceAnnulment:   encoded,
		}
		signedOps[i] = SignedOperation{
			Operation:     annulOp,
			Base64Payload: encoded,
		}
	}

	ec := newEnvelopeContext(c.cfg.Clock(), c.cfg.SignKey, signedOps)
	req := manageAnnulmentRequest{
		XmlnsCom:      xmlnsCommonURI,
		Header:        ec.header(),
		User:          ec.user(c.cfg.Login, c.cfg.Password, c.cfg.TaxNumber),
		Software:      c.cfg.Software.toXML(),
		ExchangeToken: tok.Value,
		Annulments: annulmentOperationsXML{
			Annulments: encOps,
		},
	}

	var resp schemas.ManageAnnulmentResponse
	if err := c.do(ctx, "/manageAnnulment", req, &resp); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{TransactionID: resp.TransactionID}, nil
}

// QueryTransactionStatus polls NAV for the status of a prior submission.
func (c *Client) QueryTransactionStatus(ctx context.Context, transactionID string, returnOriginal bool) (schemas.QueryTransactionStatusResponse, error) {
	if transactionID == "" {
		return schemas.QueryTransactionStatusResponse{}, errors.New("nav: transactionID is required")
	}
	ec := newEnvelopeContext(c.cfg.Clock(), c.cfg.SignKey, nil)
	req := queryTransactionStatusRequest{
		XmlnsCom:              xmlnsCommonURI,
		Header:                ec.header(),
		User:                  ec.user(c.cfg.Login, c.cfg.Password, c.cfg.TaxNumber),
		Software:              c.cfg.Software.toXML(),
		TransactionID:         transactionID,
		ReturnOriginalRequest: returnOriginal,
	}
	var resp schemas.QueryTransactionStatusResponse
	if err := c.do(ctx, "/queryTransactionStatus", req, &resp); err != nil {
		return schemas.QueryTransactionStatusResponse{}, err
	}
	return resp, nil
}

// do marshals req to XML, POSTs it to the given path, and decodes the
// response into out. If NAV returns funcCode=ERROR or HTTP non-2xx, do
// returns a *NAVError.
func (c *Client) do(ctx context.Context, path string, req any, out any) error {
	body, err := xml.Marshal(req)
	if err != nil {
		return fmt.Errorf("nav: marshal request: %w", err)
	}
	body = append([]byte(xml.Header), body...)

	if c.cfg.Debug {
		slog.Default().Info("nav: request", "path", path, "body", string(body))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("nav: build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/xml")
	httpReq.Header.Set("Accept", "application/xml")

	// Block until the rate limiter says we may proceed. Wait honours
	// ctx cancellation so a long backoff doesn't strand a caller that
	// has lost interest. Note this enforces per-process spacing only;
	// multi-replica deployments sharing one NAV technical user
	// collectively still need coordination above this layer.
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("nav: rate-limit wait: %w", err)
		}
	}

	httpResp, err := c.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("nav: http %s: %w", path, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("nav: read response: %w", err)
	}

	if c.cfg.Debug {
		slog.Default().Info("nav: response", "path", path, "status", httpResp.StatusCode, "body", string(respBody))
	}

	if httpResp.StatusCode >= 400 {
		var exc schemas.GeneralExceptionResponse
		if xml.Unmarshal(respBody, &exc) == nil && exc.Message != "" {
			return navErrorFromException(httpResp.StatusCode, exc)
		}
		// Some endpoints return the per-endpoint envelope even on 4xx;
		// try to surface the BasicResult from it.
		if br := extractBasicResult(respBody); br.FuncCode != "" {
			return navErrorFromResult(httpResp.StatusCode, br)
		}
		return &NAVError{
			HTTPStatus: httpResp.StatusCode,
			Message:    fmt.Sprintf("unexpected NAV response: %s", trim(respBody, 200)),
			Retriable:  httpResp.StatusCode >= 500,
		}
	}

	if err := xml.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("nav: parse response: %w (body=%s)", err, trim(respBody, 200))
	}
	if br := extractBasicResult(respBody); br.FuncCode == "ERROR" {
		return navErrorFromResult(httpResp.StatusCode, br)
	}
	return nil
}

// extractBasicResult does a cheap second pass to fish the <result> element
// out of any response shape.
func extractBasicResult(body []byte) schemas.BasicResult {
	type wrapper struct {
		Result schemas.BasicResult `xml:"result"`
	}
	var w wrapper
	_ = xml.Unmarshal(body, &w)
	return w.Result
}

func trim(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
