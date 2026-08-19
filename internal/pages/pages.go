// Package pages holds the domain layer of the Pages feature (docs/prd/pages.md):
// the closed panel vocabulary, the payload and spec types with their
// validation, the freshness state machine, and the payload ring's eviction
// rule.
//
// Everything here is pure. It reads no database, opens no socket and takes its
// clock as an argument, which is what lets the two rules that matter most —
// "a panel goes stale exactly at its SLA" and "the ring keeps 200 payloads or
// 7 days, whichever comes first" — be tested at their boundaries instead of by
// waiting.
//
// The design property the rest of the feature rests on: a page holds no query,
// no datasource, no connection string and no credentials. It renders the last
// payload a producer pushed. So there is nothing here that fetches anything;
// the whole package is about deciding whether what arrived is admissible, and
// what it means now.
package pages

import (
	"fmt"
	"time"
)

// Structural limits from PRD §10 and §10b.3.
//
// These are properties of the storage shape, which is why they are constants:
// changing the ring depth changes what a sparkline can draw, and changing a
// size cap changes what the database is expected to hold. The PUSH RATE limits
// are the opposite kind of number — they live in config/rate-limits.yml so they
// are visible, reviewable and adjustable in Settings without a release.
const (
	// MaxPayloadBytes caps one panel payload. Enforced in Go and never as a DB
	// CHECK: a CHECK cannot produce the 422 rejection envelope the API owes the
	// caller, and cannot be raised without a migration.
	MaxPayloadBytes = 64 * 1024

	// MaxSpecBytes caps one page spec.
	MaxSpecBytes = 256 * 1024

	// MaxPanelsPerPage — beyond this nobody reads the page anyway.
	MaxPanelsPerPage = 24

	// MaxPagesPerWorkspace is soft and admin-raisable; it exists to stop an
	// agent loop producing thousands.
	MaxPagesPerWorkspace = 100

	// MaxVersionsPerPage — every save is a version and rollback needs a target,
	// but the history is not the product.
	MaxVersionsPerPage = 50

	// RingMaxPayloads and RingMaxAge bound the per-panel payload ring, count
	// first and age second. A panel pushed every 5 s produces ~120 000 rows in
	// a week, so age alone cannot be the bound; a sparkline needs about 30
	// points, so 200 is already generous.
	RingMaxPayloads = 200
	RingMaxAge      = 7 * 24 * time.Hour
)

// ErrorCode names why a payload or spec was refused. The handler maps these
// onto HTTP: CodeTooLarge becomes the 422 rejection envelope, the rest become
// 400 with the detail attached. They are a closed set because a producer script
// reading them is doing so in a `case` statement.
type ErrorCode string

const (
	// CodeUnknownSchema — the panel schema is not a member of the closed set,
	// or is reserved but not yet producible.
	CodeUnknownSchema ErrorCode = "unknown_schema"
	// CodeTooLarge — the payload or spec exceeds its cap.
	CodeTooLarge ErrorCode = "too_large"
	// CodeInvalidJSON — the bytes are not JSON at all. Distinct from a schema
	// violation so a truncated push can be told apart from a wrong one.
	CodeInvalidJSON ErrorCode = "invalid_json"
	// CodeSchemaViolation — valid JSON that the published schema refuses.
	CodeSchemaViolation ErrorCode = "schema_violation"
	// CodeInconsistentPayload — the payload passes its schema but contradicts
	// itself, which JSON Schema cannot express: a table row keyed by a column
	// that was never declared, for instance.
	CodeInconsistentPayload ErrorCode = "inconsistent_payload"
	// CodeInvalidSpec — the page spec is malformed or breaks a declared limit.
	CodeInvalidSpec ErrorCode = "invalid_spec"
)

// ValidationError is the single error type this package returns, so a caller
// can branch on Code without string-matching a message.
type ValidationError struct {
	Code   ErrorCode
	Schema PanelSchema
	Detail string
}

func (e *ValidationError) Error() string {
	if e.Schema != "" {
		return fmt.Sprintf("pages: %s (%s): %s", e.Code, e.Schema, e.Detail)
	}
	return fmt.Sprintf("pages: %s: %s", e.Code, e.Detail)
}

func newError(code ErrorCode, schema PanelSchema, format string, args ...any) *ValidationError {
	return &ValidationError{Code: code, Schema: schema, Detail: fmt.Sprintf(format, args...)}
}
