// Package common holds the small types every wallet package shares.
package common

import "errors"

// Error is a failure the caller is expected to handle: a stable code plus a human-readable message.
// The API maps codes to HTTP statuses. Two Errors match under errors.Is when their codes are equal.
type Error struct {
	Code    string
	Message string
}

func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// CodeOf returns the code of the first *Error in err's chain, or "" when there is none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
