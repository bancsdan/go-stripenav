package navclient

import (
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// retriableErrorCodes are NAV error codes that are worth retrying after a
// backoff. The set is intentionally small — any unknown code defaults to
// non-retriable so we surface integration bugs instead of looping forever.
var retriableErrorCodes = map[string]bool{
	"OPERATION_FAILED":      true,
	"INTERNAL_ERROR":        true,
	"INVALID_REQUEST":       false,
	"INVALID_USER_RIGHT":    false,
	"INVALID_SECURITY_USER": false,
	"INVALID_SOFTWARE":      false,
	"INVALID_VERSION":       false,
}

func navErrorFromResult(httpStatus int, r schemas.BasicResult) *nav.NAVError {
	retriable := false
	if httpStatus >= 500 {
		retriable = true
	}
	if v, ok := retriableErrorCodes[r.ErrorCode]; ok {
		retriable = v
	}
	return &nav.NAVError{
		HTTPStatus: httpStatus,
		FuncCode:   r.FuncCode,
		Code:       r.ErrorCode,
		Message:    r.Message,
		Retriable:  retriable,
	}
}

func navErrorFromException(httpStatus int, e schemas.GeneralExceptionResponse) *nav.NAVError {
	retriable := httpStatus >= 500
	if v, ok := retriableErrorCodes[e.ErrorCode]; ok {
		retriable = v
	}
	return &nav.NAVError{
		HTTPStatus: httpStatus,
		FuncCode:   e.FuncCode,
		Code:       e.ErrorCode,
		Message:    e.Message,
		Retriable:  retriable,
	}
}
