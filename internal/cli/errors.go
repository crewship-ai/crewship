package cli

import (
	"errors"
	"fmt"
	"net/http"
)

// CLI exit codes. Every command exits with one of these so scripts and
// agents can branch on the class of failure without parsing error prose.
// `crewship wait` keeps its own run-outcome codes (documented on the
// command) — the codes below describe *command failures*, not run states.
const (
	ExitOK          = 0
	ExitGeneric     = 1 // unclassified error
	ExitValidation  = 2 // HTTP 400 / 422 — the request itself was invalid
	ExitNotFound    = 3 // HTTP 404
	ExitAuth        = 4 // HTTP 401 / 403, not logged in, token-host mismatch
	ExitConflict    = 5 // HTTP 409
	ExitRateLimited = 6 // HTTP 429
	ExitServer      = 7 // HTTP 5xx
	ExitConnection  = 8 // network failure before any response was read
)

// ExitCoder is implemented by errors that carry a CLI exit code.
type ExitCoder interface {
	ExitCode() int
}

// APIError is a non-2xx API response, preserving the pieces a machine
// consumer needs (status, detail, extension members) alongside the exact
// human-readable message the CLI has always printed.
type APIError struct {
	// Status is the HTTP status code of the response.
	Status int
	// Detail is the server-provided error text ({"error": …} or the
	// RFC 7807 "detail" member), without the "API error (NNN):" prefix.
	Detail string
	// Extensions is the full parsed JSON error body, so extension
	// members (e.g. missing_integrations) survive into --format json
	// error envelopes. Nil when the body wasn't JSON.
	Extensions map[string]interface{}
	// message is the rendered human string; kept byte-identical to the
	// historical "API error (NNN): …" format that scripts match on.
	message string
}

func (e *APIError) Error() string { return e.message }

// ExitCode maps the HTTP status to the CLI exit-code contract.
func (e *APIError) ExitCode() int {
	switch {
	case e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity:
		return ExitValidation
	case e.Status == http.StatusNotFound:
		return ExitNotFound
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		return ExitAuth
	case e.Status == http.StatusConflict:
		return ExitConflict
	case e.Status == http.StatusTooManyRequests:
		return ExitRateLimited
	case e.Status >= 500:
		return ExitServer
	default:
		return ExitGeneric
	}
}

// ConnectionError is a transport-level failure: the request never produced
// an HTTP response (DNS, refused connection, timeout mid-flight).
type ConnectionError struct {
	Err error
}

func (e *ConnectionError) Error() string {
	return "request failed: " + e.Err.Error()
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// ExitCode marks transport failures distinctly so retry loops can tell
// "server said no" from "never reached the server".
func (e *ConnectionError) ExitCode() int { return ExitConnection }

// ExitCode classifies the token-host guard as an auth failure: the fix is
// re-binding the credential (login / --server-allow-mismatch), not retrying.
func (e *ServerMismatchError) ExitCode() int { return ExitAuth }

// codedError attaches an explicit exit code to an arbitrary error.
type codedError struct {
	err  error
	code int
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }
func (e *codedError) ExitCode() int { return e.code }

// WithExitCode wraps err so ExitCodeFor returns code. A nil err returns
// nil, so call sites can wrap unconditionally.
func WithExitCode(err error, code int) error {
	if err == nil {
		return nil
	}
	return &codedError{err: err, code: code}
}

// NotFoundf builds a client-side not-found error that exits with
// ExitNotFound. The slug→ID resolvers short-circuit before any HTTP 404
// exists, so without this the exit-code contract silently degrades to
// ExitGeneric on the highest-traffic paths (agent/crew slug typos).
func NotFoundf(format string, args ...interface{}) error {
	return WithExitCode(fmt.Errorf(format, args...), ExitNotFound)
}

// ExitCodeFor returns the exit code for err: the innermost ExitCoder in
// the chain wins; unclassified errors are ExitGeneric; nil is ExitOK.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return ExitGeneric
}

// ErrorEnvelope is the machine-readable error shape emitted on stderr when
// the output format is json/ndjson/yaml. Success output stays on stdout;
// this envelope is the failure-side counterpart so agents never have to
// regex "API error (404)" out of prose.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the failure details inside an ErrorEnvelope.
type ErrorBody struct {
	Message    string                 `json:"message"`
	Status     int                    `json:"status,omitempty"`
	Detail     string                 `json:"detail,omitempty"`
	ExitCode   int                    `json:"exit_code"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// NewErrorEnvelope builds the structured error envelope for err,
// surfacing HTTP status/detail/extensions when err is an APIError.
func NewErrorEnvelope(err error) ErrorEnvelope {
	body := ErrorBody{
		Message:  err.Error(),
		ExitCode: ExitCodeFor(err),
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		body.Status = apiErr.Status
		body.Detail = apiErr.Detail
		body.Extensions = apiErr.Extensions
	}
	return ErrorEnvelope{Error: body}
}
