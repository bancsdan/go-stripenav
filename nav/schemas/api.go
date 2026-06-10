package schemas

import "encoding/xml"

// BasicResult is the common response wrapper used by every NAV endpoint
// to communicate overall success or failure (funcCode = OK, WARN, ERROR).
type BasicResult struct {
	FuncCode  string `xml:"funcCode"`
	ErrorCode string `xml:"errorCode,omitempty"`
	Message   string `xml:"message,omitempty"`
}

// CommonResponseHeader echoes the request id/timestamp back to the caller.
type CommonResponseHeader struct {
	RequestID      string `xml:"requestId"`
	Timestamp      string `xml:"timestamp"`
	RequestVersion string `xml:"requestVersion"`
	HeaderVersion  string `xml:"headerVersion"`
}

// TokenExchangeResponse mirrors the v3.0 <TokenExchangeResponse>.
type TokenExchangeResponse struct {
	XMLName              xml.Name             `xml:"TokenExchangeResponse"`
	Header               CommonResponseHeader `xml:"header"`
	Result               BasicResult          `xml:"result"`
	EncodedExchangeToken string               `xml:"encodedExchangeToken"`
	TokenValidityFrom    string               `xml:"tokenValidityFrom"`
	TokenValidityTo      string               `xml:"tokenValidityTo"`
}

// ManageInvoiceResponse mirrors <ManageInvoiceResponse>.
type ManageInvoiceResponse struct {
	XMLName       xml.Name             `xml:"ManageInvoiceResponse"`
	Header        CommonResponseHeader `xml:"header"`
	Result        BasicResult          `xml:"result"`
	TransactionID string               `xml:"transactionId"`
}

// ManageAnnulmentResponse mirrors <ManageAnnulmentResponse>.
type ManageAnnulmentResponse struct {
	XMLName       xml.Name             `xml:"ManageAnnulmentResponse"`
	Header        CommonResponseHeader `xml:"header"`
	Result        BasicResult          `xml:"result"`
	TransactionID string               `xml:"transactionId"`
}

// QueryTransactionStatusResponse mirrors the same-named NAV response.
type QueryTransactionStatusResponse struct {
	XMLName           xml.Name             `xml:"QueryTransactionStatusResponse"`
	Header            CommonResponseHeader `xml:"header"`
	Result            BasicResult          `xml:"result"`
	ProcessingResults ProcessingResults    `xml:"processingResults"`
}

type ProcessingResults struct {
	ProcessingResult       []ProcessingResult `xml:"processingResult"`
	OriginalRequestVersion string             `xml:"originalRequestVersion"`
	AnnulmentData          *AnnulmentData     `xml:"annulmentData,omitempty"`
}

type ProcessingResult struct {
	Index                       int                          `xml:"index"`
	BatchIndex                  *int                         `xml:"batchIndex,omitempty"`
	InvoiceStatus               string                       `xml:"invoiceStatus"` // RECEIVED, PROCESSING, SAVED, FINISHED, ABORTED
	OriginalRequest             string                       `xml:"originalRequest,omitempty"`
	TechnicalValidationMessages []TechnicalValidationMessage `xml:"technicalValidationMessages"`
	BusinessValidationMessages  []BusinessValidationMessage  `xml:"businessValidationMessages"`
	CompressedContentIndicator  bool                         `xml:"compressedContentIndicator"`
}

type TechnicalValidationMessage struct {
	ValidationResultCode string `xml:"validationResultCode"` // CRITICAL, ERROR, WARN, INFO
	ValidationErrorCode  string `xml:"validationErrorCode"`
	Message              string `xml:"message"`
}

type BusinessValidationMessage struct {
	ValidationResultCode string             `xml:"validationResultCode"`
	ValidationErrorCode  string             `xml:"validationErrorCode"`
	Message              string             `xml:"message"`
	Pointer              *ValidationPointer `xml:"pointer,omitempty"`
}

type ValidationPointer struct {
	Tag                   string `xml:"tag"`
	Value                 string `xml:"value,omitempty"`
	Line                  int    `xml:"line,omitempty"`
	OriginalInvoiceNumber string `xml:"originalInvoiceNumber,omitempty"`
}

type AnnulmentData struct {
	AnnulmentVerificationStatus string `xml:"annulmentVerificationStatus"`
	AnnulmentDecisionDate       string `xml:"annulmentDecisionDate,omitempty"`
	AnnulmentDecisionUser       string `xml:"annulmentDecisionUser,omitempty"`
}

// GeneralExceptionResponse is the body NAV returns for HTTP 4xx/5xx
// errors that are not specific to a request type.
type GeneralExceptionResponse struct {
	XMLName   xml.Name `xml:"GeneralExceptionResponse"`
	FuncCode  string   `xml:"funcCode"`
	ErrorCode string   `xml:"errorCode"`
	Message   string   `xml:"message"`
}
