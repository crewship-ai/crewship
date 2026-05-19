package lookout

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

	// ctxKeyAction overrides the default block-on-detect behaviour for
	// the input guard so a per-routine config can opt into softer
	// modes ("sanitize" or "log") without forking the guard
	// implementation. Defaults to GuardActionBlock when unset, which
	// preserves backwards-compat with every caller that pre-dates this
	// feature.
	ctxKeyAction
)

// GuardAction is the policy a routine can attach to its input-guard
// scan. The default GuardActionBlock matches Crewship's historical
// "refuse the call" behaviour. Sanitize replaces the matched span with
// a redaction marker and lets the (now-defanged) text through. Log
// emits the journal entry but passes the text through unchanged — the
// right choice when an operator wants production telemetry on injection
// attempts without breaking a noisy upstream that occasionally trips
// the heuristic on benign content.
type GuardAction string

const (
	GuardActionBlock    GuardAction = "block"
	GuardActionSanitize GuardAction = "sanitize"
	GuardActionLog      GuardAction = "log"
)

// WithAction returns a derived context carrying a non-default guard
// action. The orchestrator's RunAgent wires this from per-routine
// config so a routine flagged "log-only" doesn't refuse user prompts
// that match the heuristic.
func WithAction(ctx context.Context, action GuardAction) context.Context {
	if action == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyAction, action)
}

// ActionFromContext returns the configured guard action or
// GuardActionBlock when none was attached. Always returns a usable
// value so callers don't have to nil-check.
func ActionFromContext(ctx context.Context) GuardAction {
	if v, ok := ctx.Value(ctxKeyAction).(GuardAction); ok && v != "" {
		return v
	}
	return GuardActionBlock
}

// GuardListener is invoked synchronously when InputGuard detects a
// finding. It's the integration hook for the hooks package — callers
// that want an external system (Slack, PagerDuty, the hooks subsystem)
// notified on every guardrail trip attach a listener via
// WithGuardListener BEFORE invoking the guard. Empty listener means
// "no external notification beyond the journal entry."
//
// The listener runs in the same goroutine as the guard, so callers
// should keep it cheap or dispatch asynchronously inside. The lookout
// package deliberately doesn't goroutine the call itself: synchronous
// notification preserves the existing latency contract and lets the
// listener participate in the request's cancellation.
type GuardListener func(ctx context.Context, direction string, finding Finding)

// guardListenerKey is the context-value type for GuardListener. Kept
// as a struct{} so adding more context keys doesn't conflict.
type guardListenerKey struct{}

// WithGuardListener returns a derived context carrying fn so InputGuard
// will invoke it on every finding. Pass nil to clear an inherited
// listener — useful in tests that want to assert a code path doesn't
// fire the hook even when called from a context that would normally
// have one.
func WithGuardListener(ctx context.Context, fn GuardListener) context.Context {
	return context.WithValue(ctx, guardListenerKey{}, fn)
}

// GuardListenerFromContext extracts the listener attached by
// WithGuardListener. Returns nil when none was set — callers must
// nil-check before invoking.
func GuardListenerFromContext(ctx context.Context) GuardListener {
	v, _ := ctx.Value(guardListenerKey{}).(GuardListener)
	return v
}

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
		// Pick the highest-severity finding for the journal entry + the
		// BlockedError; this is the one the operator most cares about.
		primary := result.Findings[0]
		for _, f := range result.Findings[1:] {
			if severityRank(f.Severity) > severityRank(primary.Severity) {
				primary = f
			}
		}
		emitErr := emitGuardEntry(ctx, j, journal.EntryGuardrailInput, "input", result, primary)

		// Fire the integration hook BEFORE we branch on action. The
		// listener should see every trip even in log-mode runs — that's
		// the whole point of log mode (observability without breaking
		// the user). Done after emitGuardEntry so the journal row
		// already exists by the time the listener runs and any sync
		// downstream consumer can correlate via trace_id.
		if listener := GuardListenerFromContext(ctx); listener != nil {
			listener(ctx, "input", primary)
		}

		// Branch on the configured action. Block (default) refuses the
		// call; Sanitize masks every match in-place and returns the
		// reformed text; Log lets the original through unchanged because
		// the operator opted into observability-only mode.
		switch ActionFromContext(ctx) {
		case GuardActionSanitize:
			sanitized := sanitizeFindings(text, result.Findings)
			if emitErr != nil {
				return sanitized, emitErr
			}
			return sanitized, nil
		case GuardActionLog:
			if emitErr != nil {
				return text, emitErr
			}
			return text, nil
		default:
			blocked := &BlockedError{Direction: "input", Finding: primary}
			if emitErr != nil {
				return "", errors.Join(blocked, emitErr)
			}
			return "", blocked
		}
	}
}

// sanitizeFindings replaces each Finding.Matched span with a fixed
// "[REDACTED]" marker. We don't try to be smart about reformatting
// surrounding whitespace: the goal is to defang the injection, not to
// preserve readability for downstream prompt rendering. Replacements
// are done left-to-right; overlapping matches collapse into the
// outermost span because strings.ReplaceAll handles all instances of a
// given substring.
//
// Why not the more surgical loc-based replacement that ScanInput
// already has? The Findings expose only the matched text + position
// for the FIRST hit, not every span — running strings.ReplaceAll on
// the matched literal is good enough for the heuristic patterns we
// detect (jailbreak prose, role overrides) where a single match per
// kind is the realistic case. If a future detector returns N spans
// for the same pattern, this still defangs all of them.
func sanitizeFindings(text string, findings []Finding) string {
	out := text
	for _, f := range findings {
		if f.Matched == "" {
			continue
		}
		out = strings.ReplaceAll(out, f.Matched, "[REDACTED]")
	}
	return out
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
