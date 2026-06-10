package navclient

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

	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
	"golang.org/x/time/rate"
)

// Client is the NAV Online Számla v3.0 API client. It's an internal
// type: consumers reach it indirectly through stripenav.Handler, which
// constructs one from the nav.Config the caller supplies.
type Client struct {
	cfg     nav.Config
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
func NewClient(cfg nav.Config) (*Client, error) {
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
			r = nav.DefaultRateLimit
		}
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = nav.DefaultRateBurst
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
		Software: softwareToXML(c.cfg.Software),
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

// checkBatch validates the common per-batch constraints shared by
// SubmitInvoice and AnnulInvoice.
func checkBatch(n int) error {
	if n == 0 {
		return errors.New("nav: at least one operation is required")
	}
	if n > nav.MaxBatchSize {
		return nav.ErrBatchTooLarge
	}
	return nil
}

// SubmitInvoice submits a batch of CREATE/MODIFY/STORNO operations.
func (c *Client) SubmitInvoice(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
	if err := checkBatch(len(ops)); err != nil {
		return nav.SubmitResult{}, err
	}
	tok, err := c.ExchangeToken(ctx)
	if err != nil {
		return nav.SubmitResult{}, err
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
		Software:      softwareToXML(c.cfg.Software),
		ExchangeToken: tok.Value,
		Operations: invoiceOperationsXML{
			CompressedContent: false,
			Operations:        encOps,
		},
	}

	var resp schemas.ManageInvoiceResponse
	if err := c.do(ctx, "/manageInvoice", req, &resp); err != nil {
		return nav.SubmitResult{}, err
	}
	return nav.SubmitResult{TransactionID: resp.TransactionID}, nil
}

// AnnulInvoice submits one or more ANNUL operations.
func (c *Client) AnnulInvoice(ctx context.Context, ops []nav.AnnulmentOperation) (nav.SubmitResult, error) {
	if err := checkBatch(len(ops)); err != nil {
		return nav.SubmitResult{}, err
	}
	tok, err := c.ExchangeToken(ctx)
	if err != nil {
		return nav.SubmitResult{}, err
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
		Software:      softwareToXML(c.cfg.Software),
		ExchangeToken: tok.Value,
		Annulments: annulmentOperationsXML{
			Annulments: encOps,
		},
	}

	var resp schemas.ManageAnnulmentResponse
	if err := c.do(ctx, "/manageAnnulment", req, &resp); err != nil {
		return nav.SubmitResult{}, err
	}
	return nav.SubmitResult{TransactionID: resp.TransactionID}, nil
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
		Software:              softwareToXML(c.cfg.Software),
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
	defer func() { _ = httpResp.Body.Close() }()

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
		return &nav.NAVError{
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
	// Back up to a rune boundary so we never split a multibyte
	// character (NAV messages are Hungarian).
	for n > 0 && b[n]&0xC0 == 0x80 {
		n--
	}
	return string(b[:n]) + "…"
}
