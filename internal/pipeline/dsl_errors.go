package pipeline

import (
	"fmt"
	"strings"
)

// ValidationError is a single static-validation failure with a JSON-pointer
// style path to the offending field (#1423 item 1), e.g. "/steps/2/agent_slug"
// or "/name". Not byte-for-byte RFC 6901 (step IDs / map keys are used raw,
// without ~0/~1 escaping) — routine authors don't hand-craft "/" or "~" into
// slugs or step IDs, and stricter escaping would only make the path harder to
// read for zero real-world benefit. The intent is editor/LSP jump-to, not a
// spec-exact pointer.
type ValidationError struct {
	Path    string
	Message string
}

// Error satisfies the error interface. When Path is empty (a check that
// predates path-awareness, or a structural failure with no single field to
// point at) it degrades to just the message.
func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// ValidationErrors accumulates every static check failure Validate found in
// one pass, instead of returning on the first (#1423 item 1: a single save
// attempt against an `capabilities | claude -p` -authored routine can now
// surface every problem at once instead of round-tripping one fix per
// invocation). Order is stable — each check appends in the same sequence
// Validate historically short-circuited in — so anything that scans
// Error() for a substring (strings.Contains(err.Error(), "...")) keeps
// working whether Validate found one failure or several.
type ValidationErrors []*ValidationError

// Error joins every entry into one message. A single entry renders exactly
// as that entry's own Error() would (preserving old single-error substring
// checks); multiple entries render as a numbered, indented list.
func (v ValidationErrors) Error() string {
	switch len(v) {
	case 0:
		return ""
	case 1:
		return v[0].Error()
	}
	msgs := make([]string, len(v))
	for i, e := range v {
		msgs[i] = e.Error()
	}
	return fmt.Sprintf("%d validation errors:\n  - %s", len(v), strings.Join(msgs, "\n  - "))
}

// Unwrap exposes each entry for errors.Is / errors.As / the Go 1.20+
// multi-error unwrap protocol, so callers can pull structured
// *ValidationError values back out of a plain `error` without a type
// assertion on the (unexported-shape-risk) concrete slice type.
func (v ValidationErrors) Unwrap() []error {
	out := make([]error, len(v))
	for i, e := range v {
		out[i] = e
	}
	return out
}

// asErr returns v as an error, or nil when empty — the "return the
// accumulator" tail call every multi-error-aware check function ends with.
// A bare `return v` when v is nil would return a non-nil `error` holding a
// nil slice (the classic Go typed-nil trap); this avoids that.
func (v ValidationErrors) asErr() error {
	if len(v) == 0 {
		return nil
	}
	return v
}

// asValidationError flattens an arbitrary error return from one of the
// sibling dsl_validate_*.go check functions into the accumulator: a
// *ValidationError or ValidationErrors is merged in as-is (preserving any
// path/fuzzy-hint work already done inside the check), anything else is
// wrapped with the given fallback path.
func (v *ValidationErrors) add(fallbackPath string, err error) {
	switch e := err.(type) {
	case nil:
		return
	case *ValidationError:
		// A (*ValidationError)(nil) returned through the `error` interface
		// is the classic Go typed-nil trap: err != nil here even though
		// the check "succeeded". validateStepSlugs's signature returns
		// *ValidationError directly (not error) for exactly this reason
		// at its own call site, but this generic path still has to guard
		// it for any other *ValidationError-returning check.
		if e == nil {
			return
		}
		*v = append(*v, e)
	case ValidationErrors:
		if len(e) == 0 {
			return
		}
		*v = append(*v, e...)
	default:
		*v = append(*v, &ValidationError{Path: fallbackPath, Message: err.Error()})
	}
}
