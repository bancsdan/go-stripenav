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

// newNAVError builds the typed error from a status + funcCode/errorCode
// pair: 5xx defaults to retriable, then the documented per-code list
// overrides in either direction.
func newNAVError(httpStatus int, funcCode, code, message string) *nav.NAVError {
	retriable := httpStatus >= 500
	if v, ok := retriableErrorCodes[code]; ok {
		retriable = v
	}
	return &nav.NAVError{
		HTTPStatus: httpStatus,
		FuncCode:   funcCode,
		Code:       code,
		Message:    message,
		Retriable:  retriable,
	}
}

func navErrorFromResult(httpStatus int, r schemas.BasicResult) *nav.NAVError {
	return newNAVError(httpStatus, r.FuncCode, r.ErrorCode, r.Message)
}

func navErrorFromException(httpStatus int, e schemas.GeneralExceptionResponse) *nav.NAVError {
	return newNAVError(httpStatus, e.FuncCode, e.ErrorCode, e.Message)
}
