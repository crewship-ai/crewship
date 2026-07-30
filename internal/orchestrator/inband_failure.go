package orchestrator

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAgentInBandFailure classifies an error as "the agent reported that its own
// turn failed", as opposed to a transport/exec fault. Callers match it with
// errors.Is — through any number of fmt.Errorf("...: %w") wraps on the way up.
//
// It exists because the user-facing message quotes the CLI's own text, and
// downstream classifiers do substring matching on error strings. In particular
// internal/pipeline's isTransientRunnerError treats "500", "timeout", "eof",
// "rate limit" (and friends) as retry-worthy transports; a refusal that happens
// to read "I cannot process a list of 500 items" would otherwise be retried on
// the same tier — repeating a deterministic failure and billing for it. The
// classification must come from the error's identity, never from its prose.
var ErrAgentInBandFailure = errors.New("agent-reported in-band failure")

// inBandError carries the user-facing message while keeping the machine-readable
// identity in Unwrap, so Error() stays clean copy for chat and journal.
type inBandError struct{ msg string }

func (e *inBandError) Error() string { return e.msg }
func (e *inBandError) Unwrap() error { return ErrAgentInBandFailure }

// NewInBandFailureError wraps a user-facing message as an agent-reported
// in-band failure. This is the only constructor — production (RunAgent) and
// the tests that pin the downstream classification both go through it, so a
// test can't accidentally assert against an error shape that never ships.
func NewInBandFailureError(msg string) error { return &inBandError{msg: msg} }

// inBandFailure tracks a run-level failure that the agent CLI reported *in
// band* — inside its own event stream — while still exiting 0.
//
// Why this type exists: before it, the only thing that flipped a run to
// "error" was a non-zero exit code. Every supported CLI can end a turn with a
// refusal, an internal error, or an exhausted quota and still exit(0), saying
// so only in its final stream event. Those runs were recorded as "completed":
// the mission or routine continued on an empty/broken answer, the user saw a
// green run, and the tokens were billed. The adapters already parsed the
// signal — they just dropped it into journal metadata and nothing read it.
//
// Two signals count as run-level, and only these two:
//
//   - a terminal `result` event whose metadata carries is_error=true —
//     CLAUDE_CODE (`{"type":"result","is_error":true}`), CURSOR_CLI (same
//     shape), FACTORY_DROID (snake_case `is_error` on its `result` event),
//     CODEX_CLI (`turn.failed`), GEMINI_CLI (`status:"error"` or a non-empty
//     `error` on its `result` event).
//   - an `error` event, which every parser that emits one documents as the
//     CLI's fatal envelope — CODEX_CLI, FACTORY_DROID, GEMINI_CLI (only at
//     severity != "warning"; the parser already demotes soft blocks to
//     `system`), and OPENCODE, for which this is the *only* run-level signal
//     because its `step_finish` result envelopes always report is_error=false.
//
// Explicitly NOT run-level: a failed `tool_result` — `is_error` on a Claude
// content block, `isError` on a Droid tool_result, `status:"error"` on a Gemini
// tool_result or an OpenCode `tool_use` state, a non-zero `exit_code` on a
// Codex `command_execution` item. A grep that matched nothing, a build that
// failed and then got fixed, a 404 from a fetch: the agent sees the failure and
// works around it. Failing the run on those would redden nearly every real run.
//
// Sticky, not last-one-wins: once a CLI has reported a failed turn we do not
// let a later successful envelope erase it. Adapters emit several result
// envelopes per run (Droid emits both `completion` and `result`; OpenCode emits
// one `step_finish` per step; Codex one `turn.completed` per turn) and their
// ordering is not guaranteed across versions, so last-one-wins would silently
// drop real failures — the exact class of bug this type exists to close.
type inBandFailure struct {
	seen    bool
	subtype string
	message string
}

// observe inspects one streamed AgentEvent and records the first run-level
// failure it carries. Safe to call for every event; non-terminal events and
// tool-level failures are ignored.
func (f *inBandFailure) observe(e AgentEvent) {
	switch e.Type {
	case "result":
		meta, _ := e.Metadata.(map[string]interface{})
		if meta == nil {
			return
		}
		if isErr, _ := meta["is_error"].(bool); !isErr {
			return
		}
		subtype, _ := meta["subtype"].(string)
		f.record(subtype, e.Content)
	case "error":
		f.record("", e.Content)
	}
}

func (f *inBandFailure) record(subtype, message string) {
	if f.seen {
		return
	}
	f.seen = true
	f.subtype = subtype
	f.message = strings.TrimSpace(message)
}

// inBandDetailCap bounds how much of the CLI's own message we fold into the
// user-facing error. The full text is already in the journal; this is the chat
// bubble, so it needs a cause, not a transcript.
const inBandDetailCap = 300

// Err returns the user-facing error for a recorded in-band failure, or nil if
// none was seen. Mirrors the exit-code surface in RunAgent: the string is
// rendered directly in chat and in the journal, so it must name a cause and,
// where the CLI gave one, quote it. The returned error always satisfies
// errors.Is(err, ErrAgentInBandFailure) — that identity, not the quoted prose,
// is what downstream classifiers must key off.
func (f *inBandFailure) Err() error {
	if !f.seen {
		return nil
	}
	detail := f.message
	if len(detail) > inBandDetailCap {
		detail = detail[:inBandDetailCap] + "…"
	}
	switch {
	case detail != "" && f.subtype != "":
		return NewInBandFailureError(fmt.Sprintf("agent reported a failed run (%s): %s", f.subtype, detail))
	case detail != "":
		return NewInBandFailureError("agent reported a failed run: " + detail)
	case f.subtype != "":
		return NewInBandFailureError(fmt.Sprintf("agent reported a failed run (%s) — the CLI exited 0 but its own final event says the turn failed; check the journal for that event", f.subtype))
	}
	return NewInBandFailureError("agent reported a failed run — the CLI exited 0 but its own final event says the turn failed (a refusal, an internal CLI error, or an exhausted quota); check the journal for that event")
}
