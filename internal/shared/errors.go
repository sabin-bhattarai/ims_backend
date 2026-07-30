// Package shared holds cross-cutting primitives used by every module:
// the error taxonomy, the HTTP response envelope and list pagination.
package shared

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier. Clients (web + mobile)
// switch on these, so treat them as part of the public API contract.
type Code string

const (
	CodeValidation   Code = "VALIDATION_ERROR"
	CodeUnauthorized Code = "UNAUTHORIZED"
	CodeForbidden    Code = "FORBIDDEN"
	CodeNotFound     Code = "NOT_FOUND"
	CodeConflict     Code = "CONFLICT"
	CodeRateLimited  Code = "RATE_LIMITED"
	CodeInternal     Code = "INTERNAL_ERROR"
	CodeUnavailable  Code = "SERVICE_UNAVAILABLE"

	// Domain-specific codes.
	CodeInsufficientStock Code = "INSUFFICIENT_STOCK"
	CodeInvalidTransition Code = "INVALID_STATE_TRANSITION"
)

// Error is the canonical application error. Handlers never build HTTP
// responses by hand; they return an *Error and the error middleware renders it.
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	status  int
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// Status returns the HTTP status this error maps to.
func (e *Error) Status() int {
	if e.status == 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

// WithDetails attaches structured context (field errors, ids, quantities).
func (e *Error) WithDetails(d map[string]any) *Error {
	e.Details = d
	return e
}

// WithCause wraps the underlying error for logging. The cause is never
// serialised to the client.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

func newErr(status int, code Code, msg string) *Error {
	return &Error{Code: code, Message: msg, status: status}
}

func Validation(msg string) *Error   { return newErr(http.StatusBadRequest, CodeValidation, msg) }
func Unauthorized(msg string) *Error { return newErr(http.StatusUnauthorized, CodeUnauthorized, msg) }
func Forbidden(msg string) *Error    { return newErr(http.StatusForbidden, CodeForbidden, msg) }
func Conflict(msg string) *Error     { return newErr(http.StatusConflict, CodeConflict, msg) }
func Internal(msg string) *Error     { return newErr(http.StatusInternalServerError, CodeInternal, msg) }

func Unavailable(msg string) *Error {
	return newErr(http.StatusServiceUnavailable, CodeUnavailable, msg)
}

// RateLimited is a 429, returned by the rate-limiting middleware.
func RateLimited(msg string) *Error {
	return newErr(http.StatusTooManyRequests, CodeRateLimited, msg)
}

// NotFound builds a 404 for a named entity, e.g. NotFound("product").
func NotFound(entity string) *Error {
	return newErr(http.StatusNotFound, CodeNotFound, entity+" not found")
}

// InsufficientStock is returned when an issue/transfer would drive a stock
// item negative. Available/requested are surfaced so the UI can explain it.
func InsufficientStock(available, requested float64) *Error {
	return newErr(http.StatusConflict, CodeInsufficientStock, "insufficient stock").
		WithDetails(map[string]any{"available": available, "requested": requested})
}

// InvalidTransition is returned by the PO/SO/transfer state machines.
func InvalidTransition(from, to string) *Error {
	return newErr(http.StatusConflict, CodeInvalidTransition,
		fmt.Sprintf("cannot transition from %s to %s", from, to))
}

// AsError extracts an *Error from an error chain, or nil if there is none.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}
