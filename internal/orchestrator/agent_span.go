package orchestrator

import (
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// spanDetailScrubber redacts secret-shaped tokens from span Detail before it is
// persisted to the journal and returned by the run-detail API. Detail is derived
// from raw tool input (command / url / query / pattern), which can carry tokens,
// API keys, or credentials in flags or query strings. Created once.
var spanDetailScrubber = scrubber.New()

// RunAgentSpan is one captured INTERNAL action of an agent_run step: a single
// tool the agent invoked (Bash command, file Write/Edit/Read, an MCP tool call
// like save_routine, a web fetch). It is the leaf of the drillable run-trace
// tree — run → step → tool — and is persisted to the journal (EntryRunAgentSpan)
// and mirrored as an OTEL child span of the routine step span.
//
// The shape is deliberately small and JSON-stable: it round-trips through a
// journal payload and back out the runs API as `sub_spans`, so renaming a tag
// is a breaking change for the frontend trace builder.
type RunAgentSpan struct {
	RunID      string            `json:"run_id"`
	StepID     string            `json:"step_id"`
	Seq        int               `json:"seq"`
	Kind       string            `json:"kind"` // think|bash|write|read|edit|mcp_tool|http|tool
	Name       string            `json:"name"`
	Detail     string            `json:"detail,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	DurationMs int64             `json:"duration_ms"`
	Status     string            `json:"status"` // ok|error|running
	Attributes map[string]string `json:"attributes,omitempty"`
}

const (
	// RunAgentSpanMaxPerStep bounds how many sub-spans a single agent_run step
	// can contribute. A chatty agent doing thousands of Bash calls would
	// otherwise flood the journal; past the cap we count drops and stop
	// sinking. 200 is generous for a real multi-tool task.
	RunAgentSpanMaxPerStep = 200

	// RunAgentSpanDetailMaxBytes caps the `detail` string (the command /
	// file path) so a megabyte heredoc piped into Bash can't bloat the
	// journal row. Truncation is rune-safe and marked.
	RunAgentSpanDetailMaxBytes = 2048
)

// DeriveSpanKind maps a CLI tool name to the coarse sub-span kind the trace
// tree groups on. Unknown built-ins fall through to "tool" rather than being
// dropped — visibility beats a perfect taxonomy. MCP tools (mcp__server__name)
// are always "mcp_tool".
func DeriveSpanKind(tool string) string {
	if strings.HasPrefix(tool, "mcp__") {
		return "mcp_tool"
	}
	switch tool {
	case "Bash":
		return "bash"
	case "Write":
		return "write"
	case "Edit", "MultiEdit", "NotebookEdit":
		return "edit"
	case "Read", "NotebookRead":
		return "read"
	case "Grep", "Glob", "LS":
		return "read"
	case "WebFetch", "WebSearch":
		return "http"
	default:
		return "tool"
	}
}

// mcpShortName strips the mcp__<server>__ prefix so the trace shows
// `save_routine` rather than `mcp__crewship-routines__save_routine`.
func mcpShortName(tool string) string {
	parts := strings.Split(tool, "__")
	return parts[len(parts)-1]
}

func deriveSpanName(tool string) string {
	if strings.HasPrefix(tool, "mcp__") {
		return mcpShortName(tool)
	}
	return tool
}

// detailInputKeys are the input fields, in priority order, that best describe
// what a tool did. The first non-empty string wins as the span detail.
var detailInputKeys = []string{"command", "file_path", "path", "notebook_path", "url", "pattern", "query"}

func deriveSpanDetail(tool string, input map[string]any) string {
	for _, k := range detailInputKeys {
		if v, ok := input[k].(string); ok && v != "" {
			// Redact secrets before this raw tool input is persisted /
			// surfaced — a command flag or URL query can carry a token.
			return spanDetailScrubber.Scrub(v)
		}
	}
	// MCP tools rarely carry a path/command — fall back to the short name so
	// the detail column is never blank for them.
	if strings.HasPrefix(tool, "mcp__") {
		return mcpShortName(tool)
	}
	return ""
}

func deriveSpanAttributes(tool, kind, model string, input map[string]any) map[string]string {
	attrs := map[string]string{"tool": tool}
	if model != "" {
		attrs["model"] = model
	}
	switch kind {
	case "write", "edit", "read":
		if fp, ok := input["file_path"].(string); ok && fp != "" {
			attrs["artifact_path"] = fp
		} else if p, ok := input["path"].(string); ok && p != "" {
			attrs["artifact_path"] = p
		}
	case "http":
		if u, ok := input["url"].(string); ok && u != "" {
			if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
				attrs["host"] = parsed.Host
			}
		}
	}
	return attrs
}

// truncateDetail bounds s at RunAgentSpanDetailMaxBytes on a rune boundary and
// appends a marker. Returns (result, wasTruncated).
func truncateDetail(s string) (string, bool) {
	if len(s) <= RunAgentSpanDetailMaxBytes {
		return s, false
	}
	cut := RunAgentSpanDetailMaxBytes
	for cut > 0 && cut > RunAgentSpanDetailMaxBytes-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)", true
}

type pendingToolUse struct {
	name      string
	input     map[string]any
	startedAt time.Time
}

// AgentSpanRecorder watches an agent_run event stream and emits one
// RunAgentSpan per completed tool_use→tool_result pair. It is pure (no I/O):
// the caller supplies a sink that persists to the journal and/or OTEL. It must
// be driven from a single goroutine — the orchestrator delivers events
// serially per run, so no locking is needed.
type AgentSpanRecorder struct {
	runID, stepID string
	sink          func(RunAgentSpan)
	pending       map[string]pendingToolUse
	seq           int // sequence of the NEXT emitted span (also == count sunk)
	model         string
	dropped       int
	truncated     int
}

// NewAgentSpanRecorder returns a recorder bound to one (runID, stepID). A nil
// sink yields a no-op recorder (Observe still parses but never persists).
func NewAgentSpanRecorder(runID, stepID string, sink func(RunAgentSpan)) *AgentSpanRecorder {
	return &AgentSpanRecorder{
		runID:   runID,
		stepID:  stepID,
		sink:    sink,
		pending: make(map[string]pendingToolUse),
	}
}

// Dropped reports how many sub-spans were discarded because the per-step cap
// was already reached.
func (r *AgentSpanRecorder) Dropped() int { return r.dropped }

// Truncated reports how many sub-span details were shortened to the byte cap.
func (r *AgentSpanRecorder) Truncated() int { return r.truncated }

func metaMap(ev AgentEvent) map[string]interface{} {
	m, _ := ev.Metadata.(map[string]interface{})
	return m
}

// Observe consumes one streaming AgentEvent. tool_call events open a pending
// span; the matching tool_result closes it and (when under the cap) sinks a
// RunAgentSpan. Everything else is ignored, except the session-init system
// event which seeds the resolved model stamped onto every span's attributes.
func (r *AgentSpanRecorder) Observe(ev AgentEvent) {
	if r == nil || r.sink == nil {
		return
	}
	meta := metaMap(ev)
	switch ev.Type {
	case "system":
		if r.model == "" && meta != nil {
			if model, ok := meta["model"].(string); ok && model != "" {
				r.model = model
			}
		}
	case "tool_call":
		if meta == nil {
			return
		}
		toolID, _ := meta["tool_id"].(string)
		if toolID == "" {
			return // can't correlate a result without an id
		}
		name, _ := meta["tool_name"].(string)
		if name == "" {
			name = ev.Content
		}
		input, _ := meta["input"].(map[string]any)
		r.pending[toolID] = pendingToolUse{name: name, input: input, startedAt: ev.Timestamp}
	case "tool_result":
		if meta == nil {
			return
		}
		toolUseID, _ := meta["tool_use_id"].(string)
		if toolUseID == "" {
			return
		}
		p, ok := r.pending[toolUseID]
		if !ok {
			return // orphan result (no captured tool_call) — skip
		}
		delete(r.pending, toolUseID)

		// Enforce the per-step cap AFTER pairing so we still drain pending
		// state, but before assigning a seq so seq stays dense.
		if r.seq >= RunAgentSpanMaxPerStep {
			r.dropped++
			return
		}

		kind := DeriveSpanKind(p.name)
		input := p.input
		if input == nil {
			input = map[string]any{}
		}
		detail, truncated := truncateDetail(deriveSpanDetail(p.name, input))
		if truncated {
			r.truncated++
		}
		status := "ok"
		if isErr, _ := meta["is_error"].(bool); isErr {
			status = "error"
		}
		dur := ev.Timestamp.Sub(p.startedAt).Milliseconds()
		if dur < 0 {
			dur = 0
		}
		span := RunAgentSpan{
			RunID:      r.runID,
			StepID:     r.stepID,
			Seq:        r.seq,
			Kind:       kind,
			Name:       deriveSpanName(p.name),
			Detail:     detail,
			StartedAt:  p.startedAt,
			DurationMs: dur,
			Status:     status,
			Attributes: deriveSpanAttributes(p.name, kind, r.model, input),
		}
		r.seq++
		r.sink(span)
	}
}
