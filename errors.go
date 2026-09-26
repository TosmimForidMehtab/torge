package torge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Error is a structured application error.
//
// The public part of an Error (Status, Code, Message and Details) is written to
// clients. The internal part (Err and Meta) is only logged; it is included in
// responses only when the application runs with ExposeErrors enabled, which is
// refused in production.
//
// Errors are immutable: the With* methods return modified copies, so package
// level sentinel errors can be shared safely:
//
//	var ErrUserNotFound = torge.NotFound("USER_NOT_FOUND", "User does not exist")
//
//	return ErrUserNotFound.Wrap(err)
//
// errors.Is(err, ErrUserNotFound) matches any Error with the same Status and
// Code, including wrapped copies.
type Error struct {
	// Status is the HTTP status code.
	Status int
	// Code is a stable, machine-readable error code such as "USER_NOT_FOUND".
	Code string
	// Message is a human-readable message that is safe to show to clients.
	Message string
	// Details is optional public, structured information (for example the
	// list of invalid fields).
	Details any
	// Meta is internal diagnostic metadata. It is logged, never sent.
	Meta map[string]any
	// Err is the underlying cause. It is logged, never sent.
	Err error
}

// NewError returns an Error with the given status, code and message.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Error implements the error interface. The string contains internal details
// and is intended for logs.
func (e *Error) Error() string {
	msg := fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error with the same Status and Code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Status == e.Status && t.Code == e.Code
}

// Wrap returns a copy of e with err as its underlying cause.
func (e *Error) Wrap(err error) *Error {
	c := e.clone()
	c.Err = err
	return c
}

// WithMessage returns a copy of e with a different public message.
func (e *Error) WithMessage(msg string) *Error {
	c := e.clone()
	c.Message = msg
	return c
}

// WithDetails returns a copy of e with public details.
func (e *Error) WithDetails(details any) *Error {
	c := e.clone()
	c.Details = details
	return c
}

// WithMeta returns a copy of e with an internal metadata entry added.
func (e *Error) WithMeta(key string, value any) *Error {
	c := e.clone()
	c.Meta = make(map[string]any, len(e.Meta)+1)
	maps.Copy(c.Meta, e.Meta)
	c.Meta[key] = value
	return c
}

// IsServerError reports whether the error represents a server-side failure.
func (e *Error) IsServerError() bool { return e.Status >= 500 }

func (e *Error) clone() *Error {
	c := *e
	return &c
}

// Error codes used by the framework.
const (
	CodeBadRequest           = "BAD_REQUEST"
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	CodeNotAcceptable        = "NOT_ACCEPTABLE"
	CodeConflict             = "CONFLICT"
	CodePayloadTooLarge      = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMedia     = "UNSUPPORTED_MEDIA_TYPE"
	CodeValidation           = "VALIDATION_FAILED"
	CodeInvalidJSON          = "INVALID_JSON"
	CodeTooManyRequests      = "TOO_MANY_REQUESTS"
	CodeInternal             = "INTERNAL_ERROR"
	CodeServiceUnavailable   = "SERVICE_UNAVAILABLE"
	CodeTimeout              = "TIMEOUT"
	CodeRequestCanceled      = "REQUEST_CANCELED"
	CodeRouteNotFound        = "ROUTE_NOT_FOUND"
	CodeInvalidHost          = "INVALID_HOST"
	CodeNotImplemented       = "NOT_IMPLEMENTED"
	CodeUnprocessableContent = "UNPROCESSABLE_CONTENT"
)

// StatusClientClosedRequest is the non-standard status used when the client
// disconnected before the response was written.
const StatusClientClosedRequest = 499

// BadRequest returns a 400 error.
func BadRequest(code, message string) *Error { return NewError(http.StatusBadRequest, code, message) }

// Unauthorized returns a 401 error.
func Unauthorized(code, message string) *Error {
	return NewError(http.StatusUnauthorized, code, message)
}

// Forbidden returns a 403 error.
func Forbidden(code, message string) *Error { return NewError(http.StatusForbidden, code, message) }

// NotFound returns a 404 error.
func NotFound(code, message string) *Error { return NewError(http.StatusNotFound, code, message) }

// NotAcceptable returns a 406 error.
func NotAcceptable(code, message string) *Error {
	return NewError(http.StatusNotAcceptable, code, message)
}

// Conflict returns a 409 error.
func Conflict(code, message string) *Error { return NewError(http.StatusConflict, code, message) }

// UnprocessableEntity returns a 422 error.
func UnprocessableEntity(code, message string) *Error {
	return NewError(http.StatusUnprocessableEntity, code, message)
}

// TooManyRequests returns a 429 error.
func TooManyRequests(code, message string) *Error {
	return NewError(http.StatusTooManyRequests, code, message)
}

// Internal returns a 500 error. Its message is still public, so keep it
// generic; put diagnostic information in the wrapped error.
func Internal(code, message string) *Error {
	return NewError(http.StatusInternalServerError, code, message)
}

// ServiceUnavailable returns a 503 error.
func ServiceUnavailable(code, message string) *Error {
	return NewError(http.StatusServiceUnavailable, code, message)
}

// AsError converts any error into an *Error for rendering:
//
//   - an *Error anywhere in the chain is returned as is;
//   - validate.Errors become 422 VALIDATION_FAILED with field details;
//   - *http.MaxBytesError becomes 413;
//   - context.DeadlineExceeded becomes 504, context.Canceled 499;
//   - anything else becomes an opaque 500 that wraps the original error.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var verrs validate.Errors
	if errors.As(err, &verrs) {
		return &Error{
			Status:  http.StatusUnprocessableEntity,
			Code:    CodeValidation,
			Message: "Request validation failed",
			Details: []validate.FieldError(verrs),
			Err:     err,
		}
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return &Error{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    CodePayloadTooLarge,
			Message: fmt.Sprintf("Request body exceeds the limit of %d bytes", mbe.Limit),
			Err:     err,
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Status: http.StatusGatewayTimeout, Code: CodeTimeout, Message: "The request timed out", Err: err}
	case errors.Is(err, context.Canceled):
		return &Error{Status: StatusClientClosedRequest, Code: CodeRequestCanceled, Message: "The request was canceled", Err: err}
	}
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "Internal server error", Err: err}
}

// StatusOf returns the HTTP status an error renders with.
func StatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return AsError(err).Status
}

// ErrorBody is the JSON envelope written for errors.
type ErrorBody struct {
	Error ErrorPayload `json:"error"`
}

// ErrorPayload is the content of an error response.
type ErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Details   any    `json:"details,omitempty"`
	// Debug is present only when ExposeErrors is enabled.
	Debug *ErrorDebug `json:"debug,omitempty"`
}

// ErrorDebug carries internal error information in development.
type ErrorDebug struct {
	Error string         `json:"error,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// ProblemBody is an RFC 9457 problem-details error document.
//
// ProblemErrorHandler writes errors in this shape with Content-Type
// application/problem+json. Type stays "about:blank" per the RFC; the stable
// Torge code travels in the Code extension member so clients can match on it
// exactly like the default envelope.
type ProblemBody struct {
	Type      string      `json:"type"`
	Title     string      `json:"title"`
	Status    int         `json:"status"`
	Detail    string      `json:"detail,omitempty"`
	Instance  string      `json:"instance,omitempty"`
	Code      string      `json:"code,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
	Details   any         `json:"details,omitempty"`
	Debug     *ErrorDebug `json:"debug,omitempty"`
}

// NewProblem converts e to problem details for the request path. Title is
// the short, stable status summary; the occurrence-specific message goes
// in Detail.
func NewProblem(e *Error, instance, requestID string) ProblemBody {
	p := ProblemBody{
		Type:      "about:blank",
		Title:     firstNonEmpty(http.StatusText(e.Status), e.Code),
		Status:    e.Status,
		Detail:    e.Message,
		Code:      e.Code,
		RequestID: requestID,
		Details:   e.Details,
	}
	if instance != "" {
		p.Instance = instance
	}
	return p
}

// ProblemErrorHandler renders errors as RFC 9457 problem details. Install it
// with WithErrorHandler; the default envelope is unchanged:
//
//	app := torge.New(torge.WithErrorHandler(torge.ProblemErrorHandler))
func ProblemErrorHandler(c *Context, err error) {
	e := AsError(err)
	p := NewProblem(e, c.Path(), c.RequestID())
	if c.app != nil && c.app.opts.ExposeErrors {
		dbg := &ErrorDebug{Meta: e.Meta}
		if e.Err != nil {
			dbg.Error = e.Err.Error()
		}
		if dbg.Error != "" || len(dbg.Meta) > 0 {
			p.Debug = dbg
		}
	}
	buf, merr := json.Marshal(p)
	if merr != nil {
		DefaultErrorHandler(c, err)
		return
	}
	// Headers set earlier (Allow, WWW-Authenticate, Retry-After) are kept.
	_ = c.Bytes(e.Status, "application/problem+json", buf)
}

// ErrorHandler renders an error returned by the handler chain. It is called at
// most once per request, and only if the response has not been written yet.
type ErrorHandler func(c *Context, err error)

// DefaultErrorHandler writes the standard JSON error envelope.
func DefaultErrorHandler(c *Context, err error) {
	e := AsError(err)
	payload := ErrorPayload{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: c.RequestID(),
		Details:   e.Details,
	}
	if c.app != nil && c.app.opts.ExposeErrors {
		dbg := &ErrorDebug{Meta: e.Meta}
		if e.Err != nil {
			dbg.Error = e.Err.Error()
		}
		if dbg.Error != "" || len(dbg.Meta) > 0 {
			payload.Debug = dbg
		}
	}
	// Headers set earlier (Allow, WWW-Authenticate, Retry-After) are kept.
	_ = c.JSON(e.Status, ErrorBody{Error: payload})
}
