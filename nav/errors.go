package nav

import (
	"errors"
	"fmt"

	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// ErrBatchTooLarge is returned by SubmitInvoice/AnnulInvoice when the
// caller passes more than 100 operations in a single batch.
var ErrBatchTooLarge = errors.New("nav: per-batch operation limit (100) exceeded")

// NAVError is the typed error returned for any failed NAV response. It
// captures the HTTP status, the NAV funcCode/errorCode pair, the human
// message, and a Retriable bool derived from the documented error list.
type NAVError struct {
	HTTPStatus      int
	FuncCode        string
	Code            string
	Message         string
	Retriable       bool
	OperationErrors map[int]OperationError
}

// OperationError carries per-operation failures from a manageInvoice or
// manageAnnulment response.
type OperationError struct {
	Index        int
	BatchIndex   int
	InvoiceStatus string
	Code         string
	Message      string
}

func (e *NAVError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != "" {
		return fmt.Sprintf("nav: %s/%s (http %d): %s", e.FuncCode, e.Code, e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("nav: http %d: %s", e.HTTPStatus, e.Message)
}

// IsRetriable reports whether the caller should retry the request later.
// Network errors and 5xx responses are retriable; validation errors and
// auth errors are not.
func (e *NAVError) IsRetriable() bool {
	if e == nil {
		return false
	}
	return e.Retriable
}

// retriableErrorCodes are NAV error codes that are worth retrying after a
// backoff. The set is intentionally small — any unknown code defaults to
// non-retriable so we surface integration bugs instead of looping forever.
var retriableErrorCodes = map[string]bool{
	"OPERATION_FAILED":   true,
	"INTERNAL_ERROR":     true,
	"INVALID_REQUEST":    false,
	"INVALID_USER_RIGHT": false,
	"INVALID_SECURITY_USER": false,
	"INVALID_SOFTWARE":   false,
	"INVALID_VERSION":    false,
}

func navErrorFromResult(httpStatus int, r schemas.BasicResult) *NAVError {
	retriable := false
	if httpStatus >= 500 {
		retriable = true
	}
	if v, ok := retriableErrorCodes[r.ErrorCode]; ok {
		retriable = v
	}
	return &NAVError{
		HTTPStatus: httpStatus,
		FuncCode:   r.FuncCode,
		Code:       r.ErrorCode,
		Message:    r.Message,
		Retriable:  retriable,
	}
}

func navErrorFromException(httpStatus int, e schemas.GeneralExceptionResponse) *NAVError {
	retriable := httpStatus >= 500
	if v, ok := retriableErrorCodes[e.ErrorCode]; ok {
		retriable = v
	}
	return &NAVError{
		HTTPStatus: httpStatus,
		FuncCode:   e.FuncCode,
		Code:       e.ErrorCode,
		Message:    e.Message,
		Retriable:  retriable,
	}
}
