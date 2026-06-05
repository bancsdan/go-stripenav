package nav

import (
	"errors"
	"fmt"
)

// ErrBatchTooLarge is returned by the NAV client when more than
// MaxBatchSize operations are submitted in a single batch.
var ErrBatchTooLarge = errors.New("nav: per-batch operation limit (100) exceeded")

// NAVError is the typed error returned for any failed NAV response. It
// captures the HTTP status, the NAV funcCode/errorCode pair, the human
// message, and a Retriable bool derived from the documented error list.
// Consumers can errors.As() into this to inspect retriability or surface
// NAV-specific codes.
type NAVError struct {
	HTTPStatus int
	FuncCode   string
	Code       string
	Message    string
	Retriable  bool
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
