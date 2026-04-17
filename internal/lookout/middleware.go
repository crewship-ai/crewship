package lookout

import (
	"context"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ctxKey is a private type for context keys to avoid collisions with other
// packages that might use the same string.
type ctxKey int

const (
	// ctxKeyScope is the context.Value key under which the InputGuard /
	// OutputGuard middlewares look up the journal scope for the current
	// request. Callers MUST attach a Scope before invoking the guards
	// (typically in the HTTP handler chain that wraps the agent runner).
	ctxKeyScope ctxKey = iota
)

// Scope identifies which workspace/crew/agent the guard is acting on.
// Mirrored after journal.Scope but kept separate so callers don't have to
// import the journal package just to talk to the middleware.
type Scope struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
}

// WithScope returns a derived context carrying scope. Use in the request
// handler before calling the wrapped middleware.
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, ctxKeyScope, scope)
}

// ScopeFromContext extracts the scope previously attached with WithScope.
// Returns the zero Scope and false if no scope is present.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	v, ok := ctx.Value(ctxKeyScope).(Scope)
	return v, ok
}

// BlockedError is returned by the guards when a payload was refused. It
// carries the offending finding so callers (HTTP handlers, agent runners)
// can produce a meaningful error response without re-running the scan.
type BlockedError struct {
	// Direction is "input" or "output" — useful for response code mapping
	// (input -> 400, output -> 502 in typical HTTP layers).
	Direction string
	Finding   Finding
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("lookout: %s blocked: %s (%s)", e.Direction, e.Finding.Kind, e.Finding.Detail)
}

// IsBlocked reports whether err is or wraps a *BlockedError.
func IsBlocked(err error) bool {
	var b *BlockedError
	return errors.As(err, &b)
}

// Middleware is the function shape returned by InputGuard and OutputGuard.
// It runs the scan, emits a journal entry on Block, and returns either the
// original text unchanged (Allow), a sanitised version (Sanitize), or a
// *BlockedError. Callers wire it into whatever pipeline produces or
// consumes agent text.
type Middleware func(ctx context.Context, text string) (string, error)

// InputGuard returns a middleware that runs ScanInput over each call. On
// Block it emits an EntryGuardrailInput journal entry and returns
// *BlockedError; the caller MUST refuse to feed the text to the model.
//
// The journal emit is best-effort: a failed emit does not change the
// guard's verdict (we still block) but is logged in the returned error
// chain via errors.Join so SREs investigating a missing audit entry can
// trace it back.
func InputGuard(j journal.Emitter) Middleware {
	return func(ctx context.Context, text string) (string, error) {
		result := ScanInput(text)
		if result.Verdict != VerdictBlock {
			return text, nil
		}
		// Pick the highest-severity finding for the BlockedError; this is
		// the one the operator most cares about.
		primary := result.Findings[0]
		for _, f := range result.Findings[1:] {
			if severityRank(f.Severity) > severityRank(primary.Severity) {
				primary = f
			}
		}
		emitErr := emitGuardEntry(ctx, j, journal.EntryGuardrailInput, "input", result, primary)
		blocked := &BlockedError{Direction: "input", Finding: primary}
		if emitErr != nil {
			return "", errors.Join(blocked, emitErr)
		}
		return "", blocked
	}
}

// OutputGuard returns a middleware that scans LLM/tool output for secrets
// and prompt-leak shaped data. On Block it emits EntryGuardrailOutput and
// returns *BlockedError; on Sanitize (currently the secrets-only path) it
// returns the redacted text along with a nil error after emitting an
// info-severity journal entry so the redaction is auditable.
func OutputGuard(j journal.Emitter) Middleware {
	return func(ctx context.Context, text string) (string, error) {
		redacted, findings := Redact(text)
		if len(findings) == 0 {
			return text, nil
		}
		// Take the highest-severity finding to drive the journal entry
		// and the BlockedError shape. We still emit a single entry per
		// guard call; the payload includes the full finding list.
		primary := findings[0]
		for _, f := range findings[1:] {
			if severityRank(f.Severity) > severityRank(primary.Severity) {
				primary = f
			}
		}
		result := ScanResult{Findings: findings, Verdict: VerdictSanitize}
		emitErr := emitGuardEntry(ctx, j, journal.EntryGuardrailOutput, "output", result, primary)
		// Default policy: redact and let the (now safe) text through. The
		// caller can opt into hard-block by inspecting findings on the
		// returned text; we keep the middleware sanitising because losing
		// the entire response on a single secret leak is too disruptive.
		if emitErr != nil {
			return redacted, emitErr
		}
		return redacted, nil
	}
}

// emitGuardEntry centralises the journal emit so InputGuard and OutputGuard
// share field population. Severity mapping follows: critical -> error,
// high -> warn, medium -> notice, low -> info.
func emitGuardEntry(
	ctx context.Context,
	j journal.Emitter,
	entryType journal.EntryType,
	direction string,
	result ScanResult,
	primary Finding,
) error {
	if j == nil {
		return nil
	}
	scope, _ := ScopeFromContext(ctx)
	if scope.WorkspaceID == "" {
		// No scope means we can't satisfy the journal's required field;
		// silently skip rather than emit a malformed entry. Production
		// wiring always sets the scope; tests opt in.
		return nil
	}
	_, err := j.Emit(ctx, journal.Entry{
		WorkspaceID: scope.WorkspaceID,
		CrewID:      scope.CrewID,
		AgentID:     scope.AgentID,
		MissionID:   scope.MissionID,
		Type:        entryType,
		Severity:    journalSeverityFor(primary.Severity),
		ActorType:   journal.ActorKeeper,
		Summary:     fmt.Sprintf("blocked %s: %s (%s)", direction, primary.Kind, primary.Detail),
		Payload: map[string]any{
			"findings":  result.Findings,
			"kind":      string(primary.Kind),
			"verdict":   string(result.Verdict),
			"direction": direction,
		},
	})
	return err
}

func journalSeverityFor(s Severity) journal.Severity {
	switch s {
	case SeverityCritical:
		return journal.SeverityError
	case SeverityHigh:
		return journal.SeverityWarn
	case SeverityMedium:
		return journal.SeverityNotice
	default:
		return journal.SeverityInfo
	}
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}
