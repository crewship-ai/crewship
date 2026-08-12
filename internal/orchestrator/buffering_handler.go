package orchestrator

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/crewship-ai/crewship/internal/logcollector"
)

// Accumulator captures the optional side outputs of a buffering EventHandler:
// the streamed assistant text and the metadata map of the final "result"
// event. Both are read-only via getters so callers can pull them after a run
// without touching the handler's internals.
type Accumulator struct {
	text          strings.Builder
	resultMeta    map[string]any
	resolvedModel string
	sessionInit   map[string]any
}

// Text returns the assistant text accumulated from "text" events. It is empty
// unless the handler was built with AccumulateText enabled.
func (a *Accumulator) Text() string {
	if a == nil {
		return ""
	}
	return a.text.String()
}

// ResultMeta returns the metadata map captured from the final "result" event,
// or nil if none was seen (or CaptureResultMeta was disabled).
func (a *Accumulator) ResultMeta() map[string]any {
	if a == nil {
		return nil
	}
	return a.resultMeta
}

// ResolvedModel returns the model id the run ACTUALLY resolved to, captured
// from the CLI's session-init event (ground truth for what the API served vs
// what Crewship asked for via --model). Empty when no init event reported a
// model (non-Claude adapter, or CaptureResultMeta was disabled). Callers
// persist this on the run record so an operator can verify Opus-vs-Sonnet.
func (a *Accumulator) ResolvedModel() string {
	if a == nil {
		return ""
	}
	return a.resolvedModel
}

// SessionInit returns the full metadata map of the CLI's first session-init
// event — the run's provenance: which binary answered (claude_code_version),
// which credential path resolved (apiKeySource), the permission mode, the
// CLI's own session id, and the inventory it started with (capabilities,
// skills, tools, MCP servers and the ones it dropped). Nil when no init event
// was seen (non-Claude adapter, or CaptureResultMeta was disabled). Callers
// persist the run-scoped subset via MergeSessionInitMeta; the inventory is for
// init-time surfaces, which want the map as the CLI reported it.
func (a *Accumulator) SessionInit() map[string]any {
	if a == nil {
		return nil
	}
	return a.sessionInit
}

// BufferingHandlerOpts configures NewBufferingHandler. Every field is optional;
// the zero value yields a handler that only appends to LogBuf (when non-nil).
type BufferingHandlerOpts struct {
	// LogBuf, when non-nil, receives a logcollector.LogEntry for every event.
	// Sites that guard their buffer behind a nil logWriter pass nil here.
	LogBuf *logcollector.OutputBuffer

	// AgentSlug is stamped as the LogEntry.Agent field on every entry.
	AgentSlug string

	// AccumulateText, when true, appends the Content of every "text" event to
	// the returned Accumulator's response builder.
	AccumulateText bool

	// CaptureResultMeta, when true, stores the metadata map of the final
	// "result" event (when it is a map[string]any) on the Accumulator.
	CaptureResultMeta bool

	// OnLogError, when non-nil, is invoked with the error returned by
	// LogBuf.Append. When nil the error is silently ignored, matching sites
	// that drop the buffer write error.
	OnLogError func(error)
}

// NewBufferingHandler builds the EventHandler that every RunAgent call site
// shares: it appends a uniform LogEntry to the output buffer for each event
// and, when enabled, accumulates streamed text and captures the final result
// metadata. The returned Accumulator exposes those captures via Text() and
// ResultMeta().
//
// Per-site extras (WS broadcasts, structured part accumulation, tool
// summaries) are NOT handled here — callers wrap the returned handler and run
// their own logic before or after it, preserving their existing ordering.
func NewBufferingHandler(opts BufferingHandlerOpts) (EventHandler, *Accumulator) {
	acc := &Accumulator{}
	handler := func(event AgentEvent) {
		if opts.AccumulateText && event.Type == "text" {
			acc.text.WriteString(event.Content)
		}
		if opts.CaptureResultMeta && event.Type == "result" {
			if m, ok := event.Metadata.(map[string]any); ok {
				acc.resultMeta = m
			}
		}
		// The session-init system event carries the model the run actually
		// resolved to plus the rest of the session's provenance (see
		// adapter_claude.go). Capture the first one so the run record can
		// report actual-vs-requested model and which CLI/credential produced
		// it. Gated behind the same flag as result-meta: the sites that
		// finalize a run record (chat bridge, scheduler) are exactly the ones
		// that want it.
		if opts.CaptureResultMeta && event.Type == "system" {
			if m, ok := event.Metadata.(map[string]any); ok {
				if model, ok := m["model"].(string); ok && model != "" && acc.resolvedModel == "" {
					acc.resolvedModel = model
				}
				// Only "init" carries provenance — a mid-run "api_retry"
				// system event would otherwise take the one-shot slot and
				// lock out the init event we actually want.
				if sub, _ := m["subtype"].(string); sub == "init" && acc.sessionInit == nil {
					acc.sessionInit = m
				}
			}
		}
		if opts.LogBuf != nil {
			if err := opts.LogBuf.Append(logcollector.LogEntry{
				Timestamp: event.Timestamp,
				Level:     "info",
				Agent:     opts.AgentSlug,
				Event:     event.Type,
				Content:   event.Content,
				Metadata:  event.Metadata,
			}); err != nil && opts.OnLogError != nil {
				opts.OnLogError(err)
			}
		}
	}
	return handler, acc
}

// ParseResultUsage extracts cost and token usage from a "result" event's
// metadata map. It mirrors the hand-rolled extraction the pipeline runner used:
// total_cost_usd at the top level and input_tokens / output_tokens under a
// nested "usage" map, all expected as JSON float64. Missing fields or wrong
// types yield zero values rather than an error.
func ParseResultUsage(meta any) (costUSD float64, tokIn, tokOut int) {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		return 0, 0, 0
	}
	if v, ok := m["total_cost_usd"].(float64); ok {
		costUSD = v
	}
	if usage, ok := m["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			tokIn = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			tokOut = int(v)
		}
	}
	return costUSD, tokIn, tokOut
}

// resultUsageMetaKeys are the run-summary keys copied verbatim from a "result"
// event's metadata into a run's completed-meta map. Kept as raw values (not
// parsed) so num_turns / model_usage survive untouched.
//
// permission_denials is deliberately NOT in this list even though it belongs
// to the same envelope: these keys are copied verbatim, and a denial carries
// the full tool input the CLI refused to run. See the projection in
// MergeResultUsageMeta.
var resultUsageMetaKeys = []string{"total_cost_usd", "num_turns", "usage", "model_usage"}

// unrecognisedSkipShape is the category stored when the CLI reported skipped
// MCP servers in a shape this code could not read. It is deliberately not a
// server name: the run is degraded, and pretending to know which server would
// be worse than saying we do not.
const unrecognisedSkipShape = "unrecognized_shape"

// MergeResultUsageMeta copies the standard run-summary keys from a "result"
// event's metadata map into dst, leaving any other dst entries (e.g.
// duration_ms) intact. Keys absent from meta are skipped. This dedupes the
// identical key-copy that the chat bridge and scheduler performed inline; it
// deliberately preserves raw values rather than parsing them (use
// ParseResultUsage when you need typed cost/token numbers).
func MergeResultUsageMeta(dst map[string]any, meta any) {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		return
	}
	for _, k := range resultUsageMetaKeys {
		if v, ok := m[k]; ok {
			dst[k] = v
		}
	}
	// permission_denials answers a question a run record is otherwise read
	// wrongly for: a run the CLI refused to let act reads as a run that CHOSE
	// not to act, sending an operator after a prompt problem instead of a
	// permission rule. So the denial is worth recording — and its argument is
	// not. Each element carries the full tool_input that was refused: a Bash
	// command line, the body of a Write. That is arbitrary agent-generated
	// text, it arrives on Metadata (which the scrubber never rewrites, it only
	// rewrites Content), and this map becomes the payload of a run.completed
	// journal entry, which is HMAC-chained and append-only.
	//
	// Project to the tool name. "Bash was denied" is the diagnosis; the
	// command line is a secret we would be choosing to keep forever. Same rule
	// and same reason as mcp_server_errors below.
	if v, ok := m["permission_denials"]; ok && !emptyJSONValue(v) {
		if denials, _ := boundInitObjects(initMetaObjects(v), "tool_name"); len(denials) > 0 {
			dst["permission_denials"] = denials
		}
	}
}

// apiKeySourceMetaKey is the CLI's own (camelCase) name for the auth-path
// field on the session-init event.
const apiKeySourceMetaKey = "apiKeySource"

// sessionInitMetaKeys are the session-init provenance fields copied onto a run
// record, each paired with the name it takes there — the CLI mixes camelCase
// and snake_case in one event, and a run record that did the same would be a
// trap for whoever queries it.
//
// These four are the per-run questions: which binary answered (cli_version, the
// adapter is pinned while containers install latest — #1932), which credential
// path resolved (api_key_source), whether --dangerously-skip-permissions took
// (permission_mode), and the CLI's own correlation key for cross-referencing a
// transcript (session_id).
//
// Deliberately NOT here: capabilities, skills, tools, mcp_servers. They
// describe the SESSION, not the run — identical on every run of the same agent
// and CLI, and long. They belong on the init-time surfaces (the journal entry
// and the UI); copying them onto every run row buys nothing and costs storage
// on each one, so do not "helpfully" add them.
var sessionInitMetaKeys = []struct{ src, dst string }{
	{"claude_code_version", "cli_version"},
	{apiKeySourceMetaKey, "api_key_source"},
	{"permissionMode", "permission_mode"},
	{"session_id", "session_id"},
}

// MergeSessionInitMeta copies the run-scoped session provenance from the CLI's
// session-init metadata (Accumulator.SessionInit) into dst, leaving every other
// dst entry (duration_ms, model, usage…) intact. Keys absent from meta are
// skipped: a non-Claude adapter reports none of this, and an empty string on a
// run record reads as "asked and got nothing" rather than "never asked".
//
// apiKeySource goes through safeAPIKeySource. It is an upstream field we do not
// control and this value is PERSISTED, so only a member of the known set may be
// stored verbatim — anything else is recorded as "other" (see
// model_resolution.go for why that sanitiser exists).
//
// mcp_server_errors is copied only when it actually reports a dropped server:
// an agent that lost crewship-memory to a bad --mcp-config still exits 0, and a
// gate over this key can only key off its absence if absence keeps meaning
// "nothing was skipped".
func MergeSessionInitMeta(dst map[string]any, meta any) {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		return
	}
	for _, k := range sessionInitMetaKeys {
		v, ok := m[k.src]
		if !ok {
			continue
		}
		if k.src == apiKeySourceMetaKey {
			s, _ := v.(string) // a non-string is unrecognisable, so drop it
			src := safeAPIKeySource(s)
			if src == "" {
				continue
			}
			v = src
		}
		dst[k.dst] = v
	}
	if v, ok := m["mcp_server_errors"]; ok && !emptyJSONValue(v) {
		// Project, do not copy. This map becomes the payload of the run's
		// `run.completed` journal entry, and journal entries are HMAC-chained
		// and append-only — whatever lands here cannot be redacted later. The
		// scrubber is no help either: it rewrites an event's Content and never
		// touches Metadata.
		//
		// `message` is free text the CLI produced while parsing a config file,
		// so it is the one field here that can carry something we never chose
		// to store. The server name and the error category are what an
		// operator acts on; the message stays in the live output chunk. Same
		// projection the run.session_init entry applies, for the same reason.
		errs, _ := boundInitObjects(initMetaObjects(v), "name", "type")
		errs = dropEmptyObjects(errs)
		if len(errs) == 0 {
			// The CLI said it dropped something and we could not read the
			// details — a shape change, or fields renamed. Recording nothing
			// would be the worst outcome available: a gate over this key reads
			// absence as "nothing was skipped", so an unparsed report becomes
			// a clean run. Keep the alarm, name no server we cannot name, and
			// label why the details are missing.
			errs = []map[string]any{{"type": unrecognisedSkipShape}}
		}
		dst["mcp_server_errors"] = errs
	}
}

// emptyJSONValue reports whether v carries nothing once serialised. The adapter
// passes mcp_server_errors through as raw JSON without parsing it, so an empty
// array has to be recognised here rather than trusted to have been filtered
// upstream. Anything of an unexpected shape counts as content — better a noisy
// run record than a silently dropped report.
func emptyJSONValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case json.RawMessage:
		return emptyJSONBytes(t)
	case []byte:
		return emptyJSONBytes(t)
	case string:
		return emptyJSONBytes([]byte(t))
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func emptyJSONBytes(b []byte) bool {
	switch string(bytes.TrimSpace(b)) {
	case "", "null", "[]", "{}", `""`:
		return true
	}
	return false
}
