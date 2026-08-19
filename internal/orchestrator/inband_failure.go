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
	turns   int
	// reason/status come from Claude Code's terminal_reason + api_error_status.
	// They exist because subtype is not always a cause: a hard auth failure
	// arrives as subtype "success" with is_error true, and labelling that run
	// "(success)" reads as a parser bug rather than a diagnosis.
	reason string
	status int
}

// maxTurnsSubtype is Claude Code's result subtype for "the agent-loop turn cap
// was reached before the task finished". It gets its own copy in Err() because
// the generic message ("check the journal") tells a user nothing about a limit
// they can actually raise — and because the cap is stamped on every run
// (adapter_claude.go passes --max-turns unconditionally; unattended routine runs
// carry the tighter RoutineMaxTurns), so this is the in-band failure operators
// are most likely to meet.
const maxTurnsSubtype = "error_max_turns"

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
		// Prefer an explicit error string over the event content. Content is
		// the agent's ANSWER (Gemini puts msg.Response there, Claude
		// msg.Result), and on a failed turn an answer is not a reason —
		// reporting a partial answer as the cause of the failure is worse than
		// saying nothing, because it reads as if the agent's own words were the
		// error. Parsers stamp meta["error"] with the actual cause when they
		// have one (see parser_gemini.go).
		detail := e.Content
		if s, _ := meta["error"].(string); s != "" {
			detail = s
		}
		reason, _ := meta["terminal_reason"].(string)
		f.record(subtype, detail, metaInt(meta, "num_turns"), reason, metaInt(meta, "api_error_status"))
	case "error":
		f.record("", e.Content, 0, "", 0)
	}
}

// metaInt reads an integer out of event metadata, tolerating both the in-process
// int (parsers build the map directly) and the float64 a JSON round-trip yields.
func metaInt(meta map[string]interface{}, key string) int {
	switch v := meta[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func (f *inBandFailure) record(subtype, message string, turns int, reason string, status int) {
	if f.seen {
		return
	}
	f.seen = true
	f.subtype = subtype
	f.message = strings.TrimSpace(message)
	f.turns = turns
	f.reason = reason
	f.status = status
}

// namesNoCause lists the subtype / terminal_reason values Claude Code uses for
// a turn it considers structurally finished — including one that finished by
// failing to authenticate. Measured on two versions of the same auth failure:
//
//	2.1.204  subtype "success", terminal_reason "completed",  api_error_status 401
//	2.1.226  subtype "success", terminal_reason "api_error",  api_error_status 401
//
// Both carry the real cause in the result text and the status code, never in
// these two fields, so neither value may reach the user as a diagnosis: "failed
// run (success)" and "failed run (completed)" both read as a parser bug.
var namesNoCause = map[string]bool{"": true, "success": true, "completed": true}

// label names the cause in the user-facing error, taking the first field that
// carries one: a real error subtype, then terminal_reason, then the HTTP status
// on its own — 401 and 529 send the operator to completely different places, so
// the number is worth surfacing even when nothing else names the failure.
func (f *inBandFailure) label() string {
	if !namesNoCause[f.subtype] {
		return f.subtype
	}
	switch {
	case !namesNoCause[f.reason] && f.status > 0:
		return fmt.Sprintf("%s %d", f.reason, f.status)
	case !namesNoCause[f.reason]:
		return f.reason
	case f.status > 0:
		return fmt.Sprintf("HTTP %d", f.status)
	}
	return ""
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
	// Turn cap: name the limit and the two ways out. Deliberately does NOT
	// quote f.message — on this subtype the CLI puts its partial answer in
	// `result`, which is work-in-progress, not a cause.
	if f.subtype == maxTurnsSubtype {
		if f.turns > 0 {
			return NewInBandFailureError(fmt.Sprintf(
				"agent stopped at its turn cap (%d turns) before finishing the task — raise max_turns for this run, or split the work into smaller steps",
				f.turns,
			))
		}
		return NewInBandFailureError(
			"agent stopped at its turn cap before finishing the task — raise max_turns for this run, or split the work into smaller steps")
	}
	detail := f.message
	if len(detail) > inBandDetailCap {
		detail = detail[:inBandDetailCap] + "…"
	}
	label := f.label()
	switch {
	case detail != "" && label != "":
		return NewInBandFailureError(fmt.Sprintf("agent reported a failed run (%s): %s", label, detail))
	case detail != "":
		return NewInBandFailureError("agent reported a failed run: " + detail)
	case label != "":
		return NewInBandFailureError(fmt.Sprintf("agent reported a failed run (%s) — the CLI exited 0 but its own final event says the turn failed; check the journal for that event", label))
	}
	return NewInBandFailureError("agent reported a failed run — the CLI exited 0 but its own final event says the turn failed (a refusal, an internal CLI error, or an exhausted quota); check the journal for that event")
}
