package orchestrator

// What an exec.command journal entry — and the chat.user_message entry beside
// it — is allowed to carry.
//
// #2205 put the argv through the credential scrubber before the journal write.
// That closed the case it was opened for: a credential value the run holds, or
// anything matching a built-in pattern, no longer reaches a row that cannot be
// redacted. It could not close the general case, and was never going to:
//
//	The scrubber is defence in depth, explicitly NOT a boundary
//	(docs/security/threat-model.mdx, and the comment on
//	Scrubber.AddSecretValues). A pattern set cannot match a value nobody
//	registered and whose shape we do not know. A user who pastes an internal
//	token of an unknown shape into agent chat still lands it in a hash-chained
//	row that the GDPR erasure cascade deliberately skips
//	(internal/api/admin_gdpr.go).
//
// So the fix is not to scrub harder. It is to stop persisting the
// prompt-bearing values at all (#2215): every argv element that carries the
// system prompt or the user message is REPLACED before anything else happens,
// with a placeholder naming the element and its length. What the entry keeps is
// a bounded, typed record — adapter, model, phase, tool profile, container,
// exit code, duration, and a prompt digest + length — which answers "what ran,
// and with which prompt" without storing the prompt.
//
// The scrub still runs, on what is left. A credential can reach a NON-prompt
// argv element (an adapter --config value, a model id routed from a
// credential), and #2205's answer to that is still the right one.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

const (
	// journalArgvElemMaxChars caps a single argv element in a journal payload.
	// Generous next to what survives prompt removal (flags, a model id, a
	// container path) and small enough that no one element can dominate.
	journalArgvElemMaxChars = 512

	// journalArgvTotalMaxChars caps the whole argv. The prompt is the big
	// element today, but --config blobs and model identifiers are
	// caller-controlled too, and this table is append-only storage: the cap is
	// what makes the payload size a property of the code rather than of
	// whatever the adapter happened to build.
	journalArgvTotalMaxChars = 4096

	// journalFieldMaxChars caps every string field of the payload beside the
	// argv — the adapter, the model, the tool profile, and the per-outcome
	// strings a caller passes in (error, reason). Same reasoning and the same
	// number as the per-element argv cap: a payload that bounds the argv and
	// then copies an unbounded model identifier into a scalar next to it has
	// not bounded anything.
	journalFieldMaxChars = 512

	// journalUserMessageMaxChars caps what a chat.user_message entry persists
	// of the message body. It is the bound the entry's SUMMARY has always had;
	// the payload had none, so the shorter of the two surfaces was the only one
	// that was bounded. The full length is still recorded in length_chars, and
	// the message itself lives in the chat, which the entry references.
	journalUserMessageMaxChars = 240

	// promptSubstringMatchMinChars is the floor for matching a prompt value as
	// a SUBSTRING of an argv element. Exact matches are always honoured, at any
	// length; substring matching needs a floor so a two-word user message
	// cannot redact half the flags around it ("json" would otherwise turn
	// "stream-json" into a placeholder). The system prompt always clears it —
	// it carries the crewship preamble — and a user message short enough to
	// miss the floor is delivered as its own argv element, which the exact
	// match catches.
	promptSubstringMatchMinChars = 12
)

// journalCmdView is the only argv form allowed into a journal payload, an entry
// summary, or a log line. The truncated flag travels with the argv rather than
// beside it so a caller cannot report one without the other.
type journalCmdView struct {
	argv      []string
	truncated bool
}

// journalArgv turns the argv that will actually be exec'd into the form that
// may be persisted. Three layers, in order:
//
//  1. every prompt-bearing element is REPLACED — not scrubbed — with a
//     placeholder naming the element and its length. This is the layer that
//     holds against an opaque secret, because it does not need to recognise
//     one: the whole element goes.
//  2. what remains goes through the run's credential scrubber (#2205), for the
//     credential that reaches a non-prompt element.
//  3. what remains after that is capped, and says so.
//
// The raw argv is what gets exec'd and never leaves this process.
func journalArgv(argv []string, req AgentRunRequest, secretValues []string) journalCmdView {
	capped, truncated := capArgv(scrubArgv(redactPromptArgv(argv, req), secretValues))
	return journalCmdView{argv: capped, truncated: truncated}
}

// promptBearingValue is one piece of prompt text that must not reach a journal
// payload, with the name the placeholder reports it under.
type promptBearingValue struct {
	label string
	text  string
}

// promptBearingValues lists the prompt text this run folded into its argv.
//
// It is derived from the request rather than from per-adapter knowledge on
// purpose. Every adapter composes the prompt differently — Claude passes
// --system-prompt and the message after `--`, Gemini passes one -p blob, the
// rest concatenate "[SYSTEM]…[USER]…" — and a redactor written against those
// shapes silently stops working the day an adapter changes one. Matching on the
// values themselves covers a shape nobody has written yet.
func promptBearingValues(req AgentRunRequest) []promptBearingValue {
	sys := crewshipSystemPreamble + req.SystemPrompt
	candidates := []promptBearingValue{
		// Longest/most specific first: an element carrying both is labelled
		// with both, in this order.
		{label: "system-prompt", text: sys},
		{label: "system-prompt", text: strings.TrimSpace(sys)},
		{label: "system-prompt", text: req.SystemPrompt},
		{label: "user-message", text: req.UserMessage},
	}
	out := make([]promptBearingValue, 0, len(candidates))
	for _, c := range candidates {
		if strings.TrimSpace(c.text) != "" {
			out = append(out, c)
		}
	}
	return out
}

// redactPromptArgv returns a copy of argv with every prompt-bearing element
// replaced by a placeholder. The argv SHAPE survives — same element count, same
// flags, same order — because that is what the Crow's Nest terminal block
// renders and what an operator reads to see which flags a run was given.
func redactPromptArgv(argv []string, req AgentRunRequest) []string {
	values := promptBearingValues(req)
	if len(argv) == 0 || len(values) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	for i, arg := range argv {
		if labels := promptLabelsIn(arg, values); len(labels) > 0 {
			out[i] = promptPlaceholder(labels, arg)
			continue
		}
		out[i] = arg
	}
	return out
}

// promptLabelsIn returns the distinct labels of the prompt values arg carries,
// in the order promptBearingValues lists them.
func promptLabelsIn(arg string, values []promptBearingValue) []string {
	var labels []string
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v.label] {
			continue
		}
		if arg == v.text ||
			(utf8.RuneCountInString(v.text) >= promptSubstringMatchMinChars && strings.Contains(arg, v.text)) {
			seen[v.label] = true
			labels = append(labels, v.label)
		}
	}
	return labels
}

// promptPlaceholder names what was removed and how big it was, so the terminal
// block reads as "the prompt went here, and it was this long" rather than as a
// missing argument.
func promptPlaceholder(labels []string, arg string) string {
	return fmt.Sprintf("[PROMPT:%s omitted, %d chars]", strings.Join(labels, "+"), utf8.RuneCountInString(arg))
}

// capArgv bounds the argv and reports whether anything was cut. Per-element
// first so one long element cannot consume the whole budget and hide the flags
// after it, then a total budget with a trailing marker for the elements that
// did not fit.
func capArgv(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return argv, false
	}
	out := make([]string, 0, len(argv))
	truncated := false
	budget := journalArgvTotalMaxChars
	for i, arg := range argv {
		if budget <= 0 {
			out = append(out, fmt.Sprintf("[TRUNCATED: %d more argv elements]", len(argv)-i))
			truncated = true
			break
		}
		limit := journalArgvElemMaxChars
		if budget < limit {
			limit = budget
		}
		elem := arg
		n := utf8.RuneCountInString(elem)
		if n > limit {
			elem = string([]rune(elem)[:limit]) + fmt.Sprintf("…[truncated, %d chars]", n)
			truncated = true
			n = limit
		}
		out = append(out, elem)
		budget -= n
	}
	return out, truncated
}

// promptRef is the bounded, typed answer to "which prompt did this run use?".
//
// system_sha256 is a version identifier: two runs that quote the same digest
// ran the same system prompt, and a prompt change shows up as a digest change,
// without the prompt being stored anywhere. The lengths say how much text was
// omitted.
//
// There is deliberately NO digest of the user message. The system prompt is
// assembled by us and effectively high-entropy; a user message can be short and
// low-entropy, and a digest of "the token I pasted" is a verifier for exactly
// the value #2215 exists to keep out of this table. chat_id is the safe
// reference instead: the message lives in the chat, which is erasable.
func promptRef(req AgentRunRequest) map[string]any {
	sys := crewshipSystemPreamble + req.SystemPrompt
	digest := sha256.Sum256([]byte(sys))
	return map[string]any{
		"system_sha256": hex.EncodeToString(digest[:]),
		"system_chars":  utf8.RuneCountInString(sys),
		"user_chars":    utf8.RuneCountInString(req.UserMessage),
		"chat_id":       req.ChatID,
	}
}

// execCommandPayload builds the payload every exec.command entry carries. One
// builder for all three emit sites: the fields an operator needs to answer
// "what ran" are the same on the start, the exec-create failure and the
// terminal entry, and three hand-rolled map literals is how they drifted apart
// in the first place. extra holds the per-outcome fields (exit_code, error,
// duration_ms, reason).
func execCommandPayload(req AgentRunRequest, journalCmd journalCmdView, phase string, extra map[string]any) map[string]any {
	truncated := journalCmd.truncated
	bound := func(s string) string {
		out := truncateStr(s, journalFieldMaxChars)
		if out != s {
			truncated = true
		}
		return out
	}
	payload := map[string]any{
		"cmd":          journalCmd.argv,
		"phase":        phase,
		"adapter":      bound(req.CLIAdapter),
		"model":        bound(req.LLMModel),
		"tool_profile": bound(req.ToolProfile),
		"container_id": shortID(req.ContainerID),
		"prompt":       promptRef(req),
	}
	for k, v := range extra {
		if s, ok := v.(string); ok {
			v = bound(s)
		}
		payload[k] = v
	}
	// Written last, so extra cannot override it: a payload must not be able to
	// claim it is complete when the builder just cut something out of it.
	payload["truncated"] = truncated
	return payload
}

// journalUserMessage returns what a chat.user_message entry may persist of the
// message body: scrubbed against the run's credential values and the built-in
// patterns, then capped at journalUserMessageMaxChars — the bound the entry's
// summary already had.
//
// The scrubber is built here rather than shared for the reason
// orchestrator_run_status.go states: AddSecretValues mutates the Scrubber, so a
// package-level instance would leak this run's credential patterns into every
// other run's output. The values are collected at this point in the run rather
// than reused from the exec path because this emit fires first — before the
// sidecar handoff clears the local-model key.
func journalUserMessage(req AgentRunRequest) (preview string, truncated bool) {
	s := scrubber.New()
	if values := collectSecretValues(req); len(values) > 0 {
		s.AddSecretValues(values...)
	}
	scrubbed := s.Scrub(req.UserMessage)
	preview = truncateStr(scrubbed, journalUserMessageMaxChars)
	return preview, preview != scrubbed
}
