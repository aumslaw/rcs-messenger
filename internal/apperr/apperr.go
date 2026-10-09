// Package apperr carries domain errors across the service boundary: a stable
// Code, the HTTP status it maps to, and a message safe to show a user.
package apperr

import "net/http"

type Code string

type AppError struct {
	Code     Code
	HTTPCode int
	Message  string
	Err      error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Err }

func New(code Code, httpCode int, msg string) *AppError {
	return &AppError{Code: code, HTTPCode: httpCode, Message: msg}
}

func Wrap(err error, code Code, httpCode int, msg string) *AppError {
	return &AppError{Code: code, HTTPCode: httpCode, Message: msg, Err: err}
}

// Codes specific to one endpoint belong in that package's errors.go.
const (
	CodeBadRequest   Code = "bad_request"
	CodeUnauthorized Code = "unauthorized"
	CodeNotFound     Code = "not_found"
	CodeInternal     Code = "internal_error"
)

func BadRequest(msg string) *AppError {
	return New(CodeBadRequest, http.StatusBadRequest, msg)
}

func Unauthorized(msg string) *AppError {
	return New(CodeUnauthorized, http.StatusUnauthorized, msg)
}

func NotFound(msg string) *AppError {
	return New(CodeNotFound, http.StatusNotFound, msg)
}

func Internal(msg string) *AppError {
	return New(CodeInternal, http.StatusInternalServerError, msg)
}
