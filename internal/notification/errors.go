package notification

import (
	"errors"
	"fmt"
	"time"
)

// Code is a stable, machine-readable error identifier. Codes are part of the
// public HTTP API and must not be renamed.
type Code string

const (
	CodeInvalidRequest     Code = "INVALID_REQUEST"
	CodeEmptyMessage       Code = "EMPTY_MESSAGE"
	CodeMessageTooLarge    Code = "MESSAGE_TOO_LARGE"
	CodeAttachmentTooLarge Code = "ATTACHMENT_TOO_LARGE"
	CodeNotConfigured      Code = "NOT_CONFIGURED"
	CodeInvalidCredentials Code = "INVALID_CREDENTIALS"
	CodeInvalidDestination Code = "INVALID_DESTINATION"
	CodeRateLimited        Code = "RATE_LIMITED"
	CodeTimeout            Code = "TIMEOUT"
	CodeNetwork            Code = "NETWORK_ERROR"
	CodeDeliveryFailed     Code = "DELIVERY_FAILED"
)

// Error is a categorized failure. Message is safe to show to end users and
// API clients: it never contains credentials.
type Error struct {
	Code       Code
	Message    string
	RetryAfter time.Duration // set for CodeRateLimited when known
	Err        error         // underlying cause, already redacted
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an *Error with a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the Code of err, or CodeDeliveryFailed for uncategorized errors.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeDeliveryFailed
}

// Transient reports whether retrying later might succeed.
func (c Code) Transient() bool {
	switch c {
	case CodeRateLimited, CodeTimeout, CodeNetwork:
		return true
	}
	return false
}
