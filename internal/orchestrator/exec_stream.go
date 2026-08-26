package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/provider"
)

// endOfStreamEmitTimeout bounds the fallback context used when the request
// context was already cancelled and we still want to flush a post-mortem
// journal entry. Long enough that a healthy journal write completes, short
// enough that a stuck store can't pin the goroutine indefinitely.
const endOfStreamEmitTimeout = 5 * time.Second

// streamJSONMessage represents a line from Claude Code --output-format stream-json.
// The format varies: top-level messages have "type" like "assistant", "result", "system";
// stream events have type "stream_event" with nested "event" containing deltas.
type streamJSONMessage struct {
	// Type is deliberately the one field that is NOT tolerant, and it has to
	// stay that way: parseClaudeCodeStreamJSON uses a decoded Type as its test
	// of whether anything survived, so a tolerant Type would swallow the error
	// that keeps a structurally broken line visible as raw text. See the
	// comment on the unmarshal there.
	Type string `json:"type"`
	// Subtype is the second discriminator — the whole init branch and the
	// error_max_turns classification hang off it. See tolerantSubtype.
	Subtype tolerantSubtype `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	// For "assistant" type messages with content blocks at top level (legacy)
	Content []contentBlock `json:"content,omitempty"`
	// For "result" type. IsError, TotalCostUSD and NumTurns are tolerant for a
	// reason the isolation in parseClaudeCodeStreamJSON does not cover: keeping
	// the LINE is not the same as keeping the FIELD, and a zeroed is_error is
	// the same silent COMPLETED by a shorter route. See tolerantString.
	Result       string          `json:"result,omitempty"`
	DurationMs   float64         `json:"duration_ms,omitempty"`
	DurationAPI  float64         `json:"duration_api_ms,omitempty"`
	TotalCostUSD tolerantFloat   `json:"total_cost_usd,omitempty"`
	NumTurns     tolerantInt     `json:"num_turns,omitempty"`
	IsError      tolerantBool    `json:"is_error,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	ModelUsage   json.RawMessage `json:"modelUsage,omitempty"`
	Errors       []string        `json:"errors,omitempty"`
	// Also on "result": why the turn ended, and what the CLI refused. Not
	// derivable from Subtype — a hard auth failure reports subtype "success"
	// with IsError true and TerminalReason "api_error". Tolerant types for the
	// same reason Skills is RawMessage below, and the result line is the
	// costlier one to lose: see tolerantString.
	TerminalReason    tolerantString  `json:"terminal_reason,omitempty"`
	APIErrorStatus    tolerantInt     `json:"api_error_status,omitempty"`
	StopReason        tolerantString  `json:"stop_reason,omitempty"`
	PermissionDenials json.RawMessage `json:"permission_denials,omitempty"`
	// For "system" type with subtype "init"
	Model        string          `json:"model,omitempty"`
	Tools        []string        `json:"tools,omitempty"`
	CWD          string          `json:"cwd,omitempty"`
	MCPSrvrs     json.RawMessage `json:"mcp_servers,omitempty"`
	Plugins      json.RawMessage `json:"plugins,omitempty"`
	PluginErrors json.RawMessage `json:"plugin_errors,omitempty"`
	// Session provenance, all on system/init. ClaudeCodeVersion is the one
	// that matters most: the adapter is pinned to an npm version in
	// cli_adapter_versions_test.go while containers install latest, and
	// without this field nothing in a run says which of the two answered.
	// Capabilities is the CLI's own list of protocol behaviours (e.g.
	// "interrupt_receipt_v1") — feature-detect on it rather than on a version
	// string. Documented upstream as an array of strings, and carried
	// tolerantly anyway: "documented" is the assurance that let a pinned
	// adapter version drift for a hundred releases (#1932), and this field has
	// no consumer that a dropped entry would break. MCPServerErrors
	// (v2.1.219+) reports --mcp-config entries dropped by validation; the run
	// continues and exits 0 without them.
	ClaudeCodeVersion string          `json:"claude_code_version,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
	APIKeySource      string          `json:"apiKeySource,omitempty"`
	PermissionMode    string          `json:"permissionMode,omitempty"`
	Capabilities      tolerantStrings `json:"capabilities,omitempty"`
	MCPServerErrors   json.RawMessage `json:"mcp_server_errors,omitempty"`
	// The plain `string` provenance fields above — SessionID, APIKeySource,
	// PermissionMode, ClaudeCodeVersion, and Model / Result / CWD elsewhere on
	// this struct — stay strict on purpose, and the decode marker is what makes
	// that safe. Every one of them is written into metadata behind an `if != ""`
	// guard, so a field we could not read becomes an ABSENCE, not a false
	// statement; that is the opposite of is_error, whose zero value actively
	// asserts the run was fine. And there is no shape-preserving reading of
	// {"id":"claude-opus-5"} as a model string — recovering one means guessing a
	// key, which is how the wrong provenance gets recorded as fact. What was
	// missing was any record that we could not read them, and that is what
	// parseClaudeCodeStreamJSON's decode marker now supplies.
	//
	// Skills is RawMessage, not []string, deliberately: the field is not in
	// the published stream reference, so its shape is not a promise. A typed
	// field that stopped matching would fail the unmarshal of the ENTIRE init
	// line and dump it to the UI as raw text — a large blast radius for a
	// field we only pass through.
	Skills json.RawMessage `json:"skills,omitempty"`
	// For "system" type with subtype "api_retry" (Anthropic 2.1.x ships this
	// as a separate event when auth/rate/billing/server retries kick in).
	// Surface to journal so backoff investigations have data; pre-fix parser
	// dropped these to the default branch and Crow's Nest never saw them.
	Attempt      int     `json:"attempt,omitempty"`
	MaxRetries   int     `json:"max_retries,omitempty"`
	RetryDelayMs float64 `json:"retry_delay_ms,omitempty"`
	ErrorStatus  int     `json:"error_status,omitempty"`
	ErrorMessage string  `json:"error,omitempty"`
	// For stream_event type (--include-partial-messages)
	Event *streamEvent `json:"event,omitempty"`
	// Present on every line emitted by a nested subagent (Task tool): the
	// tool_use id of the Task call that spawned it. Lets the UI scope subagent
	// thinking/tool activity under its parent instead of flattening it into the
	// main stream. Empty on top-level (parent agent) lines.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
}

// The tolerant scalar types below defend the same thing Skills defends by being
// a RawMessage — but they defend the field's MEANING, which is a different job
// from the one parseClaudeCodeStreamJSON's per-field isolation does.
//
// Isolation keeps the line: a field whose Go type stopped matching upstream is
// skipped and everything else on the envelope still arrives. That is the whole
// answer for a field we only pass through. It is NOT the answer for a field
// somebody acts on, because "skipped" means "zero", and the zero of is_error is
// false — inBandFailure.observe keys on meta["is_error"], so a run that failed
// to authenticate is finalised COMPLETED just as surely as if the whole line
// had been lost. Same for total_cost_usd (a run finalised at $0.00) and
// api_error_status (401 vs 529 is the difference between "fix the credential"
// and "retry"). Where the neighbouring shape still says the same thing —
// "true" is true, "0.42" is 0.42, "401" is 401 — read it rather than drop it.
//
// These types never return an error, because an error from a custom
// UnmarshalJSON aborts the whole decode and would put us back where we started.
// They accept the documented shape, accept the neighbouring one where it still
// means something, and reduce anything else to a zero value. Call sites keep
// their typed use and convert back to the plain Go type on the way into event
// metadata, which crosses into the journal and is type-asserted there.
type tolerantString string

func (s *tolerantString) UnmarshalJSON(b []byte) error {
	*s = ""
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		return nil
	}
	if t[0] == '"' {
		var str string
		if json.Unmarshal(b, &str) == nil {
			*s = tolerantString(str)
		}
		return nil
	}
	// A bare number or bool still reads as a label, so keep it verbatim. An
	// object or array does not — dropping it beats pushing a JSON blob into a
	// user-facing "failed run (...)" string.
	if t[0] != '{' && t[0] != '[' {
		*s = tolerantString(t)
	}
	return nil
}

// tolerantSubtype carries `subtype`, and it is a type of its own rather than
// another tolerantString because subtype is not payload — it is the second
// DISCRIMINATOR. Every field above costs one field when it cannot be read;
// subtype costs a whole branch. Measured against the shipped parser:
//
//	{"type":"system","subtype":["init"],"model":"claude-opus-5",
//	 "claude_code_version":"2.1.226",
//	 "mcp_server_errors":[{"name":"crewship-memory","type":"invalid_config"}]}
//	  -> the emitted event's entire metadata was  map[subtype:]
//
// Type decoded, so the line-level isolation correctly kept the line; Subtype
// zeroed, so `switch msg.Subtype` matched nothing, the init branch never ran and
// none of the provenance was copied — no session_init journal entry, and every
// surface showing a healthy run while the CLI was reporting it had dropped an
// MCP server. With no raw line in the transcript either, that is worse than the
// pre-tolerance behaviour it replaced. On `result` the same field carries the
// error_max_turns classification and the label inBandFailure.Err() shows a user.
//
// Two things follow from being a discriminator, and they are why tolerantString
// is not enough:
//
//   - a one-element array is unwrapped. tolerantString drops arrays because a
//     JSON blob rendered into "failed run (…)" reads worse than nothing; a
//     discriminator has no such problem — ["init"] names exactly one branch, and
//     the alternative is losing everything the branch would have copied. Two
//     elements is not unwrapped: picking one would be a guess about which branch
//     the CLI meant, and guessing a branch is how the wrong provenance gets
//     recorded as fact.
//   - anything still unreadable sets `unreadable`, which the parser turns into
//     the decode marker. A tolerant type that quietly returns "" would just move
//     the bug: "" is also what a CLI that sent no subtype produces, so silence
//     here is indistinguishable from absence — the exact failure this whole
//     round is about.
type tolerantSubtype struct {
	value      string
	unreadable bool
}

func (s *tolerantSubtype) UnmarshalJSON(b []byte) error {
	*s = tolerantSubtype{}
	t := strings.TrimSpace(string(b))
	// Absent and null are not failures: plenty of envelopes carry no subtype.
	if t == "" || t == "null" {
		return nil
	}
	if t[0] == '[' {
		var elems []string
		if json.Unmarshal(b, &elems) == nil && len(elems) == 1 {
			s.value = elems[0]
			return nil
		}
		s.unreadable = true
		return nil
	}
	// A string, number or bool: tolerantString already keeps whichever of those
	// arrives, verbatim, and it never errors. An explicit "" is a value the CLI
	// sent, not a value we failed to read, so it is not flagged.
	var label tolerantString
	_ = label.UnmarshalJSON(b)
	if label == "" && t != `""` {
		s.unreadable = true
		return nil
	}
	s.value = string(label)
	return nil
}

// String is what every call site uses. The value lands in event metadata, which
// crosses into the journal and is type-asserted as a plain string there —
// inBandFailure.observe keys the failure classification off it and
// NewBufferingHandler gates the session-init capture on it — so the wrapper must
// never escape this package's decode step.
func (s tolerantSubtype) String() string { return s.value }

// tolerantNumber reads a number that may have arrived quoted. ParseFloat rather
// than Atoi: JSON has one number type, so a status can legitimately arrive as
// 401.0 or 4.01e2.
func tolerantNumber(b []byte) (float64, bool) {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		return 0, false
	}
	if t[0] == '"' {
		var str string
		if json.Unmarshal(b, &str) != nil {
			return 0, false
		}
		t = strings.TrimSpace(str)
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

type tolerantInt int

func (n *tolerantInt) UnmarshalJSON(b []byte) error {
	*n = 0
	if f, ok := tolerantNumber(b); ok {
		*n = tolerantInt(f)
	}
	return nil
}

// tolerantFloat carries total_cost_usd. A quoted amount is still the amount,
// and this is the only field the run's cost is recorded from.
type tolerantFloat float64

func (n *tolerantFloat) UnmarshalJSON(b []byte) error {
	*n = 0
	if f, ok := tolerantNumber(b); ok {
		*n = tolerantFloat(f)
	}
	return nil
}

// tolerantBool carries is_error — the one field on the result envelope that
// decides whether a run is recorded as a failure at all.
type tolerantBool bool

func (v *tolerantBool) UnmarshalJSON(b []byte) error {
	*v = false
	t := strings.TrimSpace(string(b))
	if t == "true" {
		*v = true
		return nil
	}
	if t != "" && t[0] == '"' {
		// ParseBool so "true"/"True"/"1" all land where they obviously mean to.
		var str string
		if json.Unmarshal(b, &str) == nil {
			if parsed, err := strconv.ParseBool(strings.TrimSpace(str)); err == nil {
				*v = tolerantBool(parsed)
			}
		}
		return nil
	}
	// A number reads the way C reads one: nonzero is the failure. Everything
	// else — null, an object, a word we do not recognise — stays false, and
	// that direction is deliberate: an ABSENT is_error is the normal success
	// case, so defaulting the unknown to true would fail every healthy run.
	if f, ok := tolerantNumber(b); ok {
		*v = f != 0
	}
	return nil
}

type tolerantStrings []string

func (s *tolerantStrings) UnmarshalJSON(b []byte) error {
	*s = nil
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		// Not an array at all — the field is gone, the line survives.
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, el := range raw {
		var str string
		// An entry that grew into an object is skipped rather than guessed at:
		// this is a pass-through list, and inventing a key to read would be a
		// second guess about a shape we already got wrong once.
		if json.Unmarshal(el, &str) == nil {
			out = append(out, str)
		}
	}
	if len(out) > 0 {
		*s = out
	}
	return nil
}

// nestedMessage extracts content blocks from the "message" field if present.
// Claude Code stream-json wraps assistant content in {"type":"assistant","message":{"content":[...]}}.
type nestedMessage struct {
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Name      string       `json:"name,omitempty"`
	ID        string       `json:"id,omitempty"`
	Input     any          `json:"input,omitempty"`
	ToolUseID string       `json:"tool_use_id,omitempty"`
	IsError   bool         `json:"is_error,omitempty"`
	Source    *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type streamEvent struct {
	Type  string      `json:"type"`
	Delta *eventDelta `json:"delta,omitempty"`
}

type eventDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

// streamOutput drains the exec's combined stdout+stderr, dispatching parsed
// events to handler as it goes. It returns that same raw capture (capped to
// captureCap, unscrubbed) so a caller that sees a failing exit code afterward
// — RunAgent, once ExecInspect reports the exit status — can attach the
// container's own diagnostic text to the error instead of only the exit
// code. The 16 KB cap and the capture buffer itself already existed for the
// Crow's Nest end-of-stream journal entry below; this just stops throwing
// the same bytes away a second time.
func (o *Orchestrator) streamOutput(ctx context.Context, result *provider.ExecResult, req AgentRunRequest, handler EventHandler) string {
	var closeOnce sync.Once
	closeReader := func() {
		closeOnce.Do(func() {
			result.Reader.Close()
		})
	}
	defer closeReader()

	go func() {
		<-ctx.Done()
		closeReader()
	}()

	scanner := bufio.NewScanner(result.Reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	adapter := getAdapter(req.CLIAdapter)
	useStreamJSON := adapter.UseStreamJSON()

	// Per-stream parser state: adapters whose line parsing carries cross-line
	// state (OpenCode's accumulated-text dedup) hand out a parser scoped to
	// this stream, so state cannot leak across runs — or across in-process
	// test iterations under `go test -count>1` — through the stateless
	// adapter singleton (#1235). Stateless adapters keep using ParseStreamLine.
	parseLine := adapter.ParseStreamLine
	if f, ok := adapter.(streamLineParserFactory); ok {
		parseLine = f.NewStreamLineParser()
	}

	// Track whether the CLI delivered a terminal envelope. Some CLIs
	// (observed: opencode, anomalyco/opencode#26855) can exit before
	// emitting their final result event; without one, run finalization
	// records no usage and consumers that key off the terminal envelope see
	// a run that never "ended". The wrapped handler feeds the synthesis
	// check after the read loop.
	sawResult := false
	sawError := false
	// PR-F4 "scan path 2" (#947): every adapter's parser emits tool_result
	// events through this wrapper — it is the single production seam (the
	// parseLine resolved above has no other caller) — so the MINJA tool-return scan
	// runs here for the parsers that emit directly. Claude's parser routes
	// its blocks through emitToolResultBlock (scan path 1, same scan
	// function), so it is skipped to avoid double-scanning.
	scanHere := !adapterSelfScansToolResults(adapter)
	streamHandler := handler
	if useStreamJSON && handler != nil {
		streamHandler = func(e AgentEvent) {
			if scanHere {
				e = scanToolResultEvent(e)
			}
			switch e.Type {
			case "result":
				sawResult = true
			case "error":
				sawError = true
			}
			handler(e)
		}
	}

	// Crow's Nest: capture the first 16 KB of raw stdout+stderr so the live
	// terminal panel can show a replayable snapshot. We deliberately do NOT
	// emit per-line — at 50 lines/sec that would flood the journal. The live
	// WebSocket stream (handler → wsHub) already carries real-time output to
	// the UI; this journal entry is the persistence + replay layer, so a
	// single end-of-stream summary is the right grain. totalBytes records
	// the full byte count (un-truncated) so consumers know how much was
	// dropped from the snapshot.
	const captureCap = 16 * 1024 // 16 KB cap for in-memory buffer
	captureBuf := make([]byte, 0, captureCap)
	var totalBytes int64

	for scanner.Scan() {
		// scanner.Bytes() aliases the scanner's internal buffer, so it's
		// valid only until the next Scan(). handleStreamJSONLine consumes
		// the slice synchronously (json.Unmarshal copies strings out), and
		// the non-JSON fallback below converts to string immediately.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Accumulate for the end-of-stream journal emit. Once the buffer is
		// full we still count totalBytes so the summary reflects the real
		// volume even though the sample is capped.
		totalBytes += int64(len(line)) + 1 // +1 for the newline the scanner strips
		if len(captureBuf) < captureCap {
			remaining := captureCap - len(captureBuf)
			if len(line) <= remaining {
				captureBuf = append(captureBuf, line...)
				if len(captureBuf) < captureCap {
					captureBuf = append(captureBuf, '\n')
				}
			} else {
				captureBuf = append(captureBuf, line[:remaining]...)
			}
		}

		if useStreamJSON {
			parseLine(line, streamHandler)
		} else {
			if handler != nil {
				handler(AgentEvent{
					Type:      "text",
					Content:   string(line) + "\n",
					Timestamp: time.Now(),
				})
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}

	// Terminal-envelope resilience: the CLI produced output but exited
	// without a result event (and without a fatal error event, which owns
	// its own finalization path). Synthesize a non-error terminal result so
	// downstream finalization has an envelope — usage stays absent, which
	// ParseResultUsage reads as zeros. Flagged via subtype so observability
	// can count occurrences per adapter.
	if useStreamJSON && handler != nil && !sawResult && !sawError &&
		ctx.Err() == nil && totalBytes > 0 {
		o.logger.Warn("stream ended without terminal result — synthesizing",
			"agent_id", req.AgentID,
			"adapter", req.CLIAdapter,
		)
		handler(AgentEvent{
			Type: "result",
			Metadata: map[string]interface{}{
				"subtype":  "stream_eof_synthetic",
				"is_error": false,
			},
			Timestamp: time.Now(),
		})
	}

	// Diagnostic Warn when an agent run ends without producing any
	// output. The cause is usually one of:
	//   - prompt-budget pressure on a small model (system prompt size
	//     plus history pushed the assistant response to 0 tokens)
	//   - a safety refusal that the adapter swallowed without surfacing
	//   - the agent CLI binary exited cleanly with no stdout
	// We can't auto-classify the case here, but logging the prompt size
	// + adapter alongside agent_id gives the operator the correlation
	// signal they need when triaging issue-#545 reports. The journal
	// emit below carries the full stdout+stderr capture (which will be
	// empty in this case too) so a post-mortem has both signals.
	if totalBytes == 0 && ctx.Err() == nil {
		o.logger.Warn("agent run produced no stdout (#545)",
			"agent_id", req.AgentID,
			"agent_slug", req.AgentSlug,
			"agent_role", req.AgentRole,
			"adapter", req.CLIAdapter,
			"system_prompt_bytes", len(req.SystemPrompt),
		)
	}

	// End-of-stream Crow's Nest emit. We run unconditionally (even when
	// totalBytes is 0) because an empty-output run is still interesting for
	// debugging — the UI can render "agent produced no stdout" explicitly
	// instead of showing a hanging block. When the request context was
	// cancelled (user pressed stop) we still want the capture recorded for
	// post-mortem, but the fallback must be bounded — a fresh
	// context.Background() can pin the goroutine forever if the journal
	// store is slow or stuck. ExecID lives on the provider ExecResult so we
	// record it in the payload for correlation with the exec.command end
	// event.
	emitCtx := ctx
	if emitCtx.Err() != nil {
		var cancelEmit context.CancelFunc
		emitCtx, cancelEmit = context.WithTimeout(context.Background(), endOfStreamEmitTimeout)
		defer cancelEmit()
	}
	payload := map[string]any{
		"output":      string(captureBuf),
		"total_bytes": totalBytes,
		"truncated":   totalBytes > int64(len(captureBuf)),
	}
	if result != nil && result.ExecID != "" {
		payload["exec_id"] = result.ExecID
	}
	_, _ = o.getJournal().Emit(emitCtx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "exec.output_chunk",
		Severity:    "info",
		ActorType:   "sidecar",
		ActorID:     req.AgentID,
		Summary:     fmt.Sprintf("%s stdout+stderr capture (%d bytes)", req.AgentSlug, totalBytes),
		Payload:     payload,
		Refs:        map[string]any{"chat_id": req.ChatID},
	})

	return string(captureBuf)
}

// emitToolResultBlock sends a tool_result event for the given content block.
//
// PR-F4 — tool-return scan (MINJA-style query-time injection defence):
// tool results that flow back into the agent are an unsanitised inbound
// surface — a poisoned web fetch, shell exec, or MCP server response can
// embed prompt-injection text that the model would treat as a system
// instruction on the next turn. We run the same scanner the memory.read
// path uses; on a hit, we replace the block text with a [BLOCKED ...]
// placeholder and tag the event metadata so journal/UI can render the
// quarantine state.
//
// Scope (this is "scan path 1" — Claude assistant/tool_result blocks
// routed through this helper). Per-adapter tool_result emission in
// parser_codex.go / parser_gemini.go / parser_droid.go / parser_cursor.go
// / parser_opencode.go emits directly and is covered by "scan path 2":
// the streamHandler wrapper in streamOutput runs the same
// scanToolResultEvent on every tool_result event those parsers emit
// (#947). Both paths share one scan implementation so they can't drift.
//
// Quarantine to disk is intentionally NOT attempted here: tool results
// are ephemeral (no canonical on-disk source path), and the orchestrator
// doesn't have an AgentContext handle in this scope. The placeholder is
// what matters — the poisoned text never reaches the model. Downstream
// journal emission carries the scan category + pattern via the event
// metadata.
func emitToolResultBlock(block contentBlock, parentID string, handler EventHandler) {
	meta := map[string]interface{}{}
	if block.ToolUseID != "" {
		meta["tool_use_id"] = block.ToolUseID
	}
	if parentID != "" {
		meta["parent_tool_use_id"] = parentID
		meta["subagent"] = true
	}
	// Surface tool failures so the run-trace sub-span recorder (and the UI)
	// can mark the span errored. Only stamped when true so the common
	// success case keeps the historic metadata shape byte-for-byte.
	if block.IsError {
		meta["is_error"] = true
	}
	handler(scanToolResultEvent(AgentEvent{
		Type:      "tool_result",
		Content:   block.Text,
		Metadata:  meta,
		Timestamp: time.Now(),
	}))
}

// adapterSelfScansToolResults reports whether the adapter's parser already
// routes its tool_result blocks through emitToolResultBlock (scan path 1),
// in which case the streamOutput chokepoint must not scan again. Only the
// Claude parser does; every other adapter emits tool_result directly and
// relies on scan path 2. The TestToolResultScan_AllAdapters matrix guards
// this mapping — a new adapter that self-scans must be added here AND
// asserted there.
func adapterSelfScansToolResults(a CLIAdapter) bool {
	return a.Name() == "CLAUDE_CODE"
}

// scanToolResultEvent is the shared MINJA tool-return scan (PR-F4).
// Non-tool_result events pass through untouched. On a scanner hit the
// poisoned content is replaced with a safe placeholder naming category +
// pattern (matches the memory read-path UX) and the event metadata is
// tagged so journal/UI can render the quarantine state.
//
// Quarantine to disk is intentionally NOT attempted here: tool results
// are ephemeral (no canonical on-disk source path) and this scope has no
// AgentContext handle. The placeholder is what matters — the poisoned
// text never reaches the model's re-injected context.
func scanToolResultEvent(e AgentEvent) AgentEvent {
	if e.Type != "tool_result" {
		return e
	}
	hit := memory.ScanContent(e.Content)
	if hit == nil {
		return e
	}
	e.Content = fmt.Sprintf(
		"[BLOCKED: tool_result scan hit category=%s pattern=%s. "+
			"Original tool output was suppressed because it contained "+
			"a prompt-injection or exfiltration signature. "+
			"This placeholder is a safe substitute.]",
		hit.Category, hit.Pattern,
	)
	meta, _ := e.Metadata.(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["scan_quarantined"] = true
	meta["scan_category"] = hit.Category
	meta["scan_pattern"] = hit.Pattern
	e.Metadata = meta
	return e
}

// emitImageBlock sends an image event for the given content block.
func emitImageBlock(block contentBlock, parentID string, handler EventHandler) {
	if block.Source != nil && block.Source.Data != "" {
		meta := map[string]interface{}{
			"media_type": block.Source.MediaType,
		}
		if parentID != "" {
			meta["parent_tool_use_id"] = parentID
			meta["subagent"] = true
		}
		handler(AgentEvent{
			Type:      "image",
			Content:   block.Source.Data,
			Metadata:  meta,
			Timestamp: time.Now(),
		})
	}
}

// handleStreamJSONLine kept as a thin wrapper around parseClaudeCodeStreamJSON
// so existing tests in exec_test.go that call o.handleStreamJSONLine directly
// keep working unchanged. The actual per-adapter dispatch happens in
// streamOutput above via adapter.ParseStreamLine.
func (o *Orchestrator) handleStreamJSONLine(line []byte, handler EventHandler) {
	parseClaudeCodeStreamJSON(line, handler)
}
