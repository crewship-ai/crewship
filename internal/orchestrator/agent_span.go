package orchestrator

import (
	"encoding/json"
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
	Kind       string            `json:"kind"` // think|bash|db|write|read|edit|mcp_tool|http|tool
	Name       string            `json:"name"`
	Detail     string            `json:"detail,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	DurationMs int64             `json:"duration_ms"`
	Status     string            `json:"status"` // ok|error|running
	Attributes map[string]string `json:"attributes,omitempty"`
	// Input is the FULL tool input marshalled to JSON (scrubbed + capped) — the
	// "args" the agent invoked the tool with. Detail carries only the single
	// most-descriptive field (command / path); Input carries everything else
	// (a Bash `description`, a Write `content`, an MCP tool's params). Empty
	// when the tool had no input args. This is the "what did it run" half of
	// #847; Output is the "what did it return" half.
	Input string `json:"input,omitempty"`
	// Output is the tool_result body (scrubbed + capped tail) — the mechanical
	// result of the call (a script's stdout, a file's contents, an MCP reply).
	// Without it you can see that `python3 parse.py` ran but never what it
	// returned. Empty when the tool produced no textual result.
	Output string `json:"output,omitempty"`
	// InputTruncated / OutputTruncated flag that the capped Input/Output was
	// cut at its byte cap — surfaced to the UI as a "truncated" chip so a
	// result cut mid-JSON reads as bounded-on-purpose, not a mystery. The full
	// output survives on the step's Output tab (run.step_outputs).
	InputTruncated  bool `json:"input_truncated,omitempty"`
	OutputTruncated bool `json:"output_truncated,omitempty"`
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

	// RunAgentSpanInputMaxBytes caps the captured Input (full args JSON). The
	// args are context (a Write's target path, a Bash `description`) — 2 KB is
	// plenty; a Write's large `content` arg doesn't need to round-trip whole.
	RunAgentSpanInputMaxBytes = 2048

	// RunAgentSpanOutputMaxBytes caps the captured Output (tool_result tail).
	// Deliberately roomier than the input cap: the output IS the deliverable
	// this feature exists to surface — a strict-JSON result (e.g. a month of
	// parsed transactions) must survive whole, and a truncated JSON body is
	// unparseable (the UI drops from the JSON viewer to plain text). 16 KB
	// holds a substantial structured result; the 200-span/step cap still
	// bounds the pathological chatty-agent case, and anything past the cap is
	// flagged (OutputTruncated) with the full text available on the step's
	// Output tab. A `cat bigfile` is still bounded here, by design.
	RunAgentSpanOutputMaxBytes = 16 * 1024
)

// DeriveSpanKind maps a tool invocation to the coarse sub-span kind the trace
// tree groups on. Unknown built-ins fall through to "tool" rather than being
// dropped — visibility beats a perfect taxonomy. MCP tools (mcp__server__name)
// are "mcp_tool" unless the server names a datastore.
//
// `input` is the raw tool-input map (nil is fine — callers that only hold a
// tool name pass nil and get the name-derived kind). It is needed because a
// datastore call is invisible in the tool NAME: `psql -c "delete from …"` and
// `ls` are both the "Bash" tool, and telling them apart is the whole point of
// the "db" kind. Classification stays here, in one function, rather than in a
// sibling the recorder could forget to call.
func DeriveSpanKind(tool string, input map[string]any) string {
	// Checked before the MCP branch: a Postgres MCP server is a database
	// action first and an MCP call second.
	if dbEngineForCall(tool, input) != "" {
		return "db"
	}
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

// dbEngines maps a datastore CLI executable — or an MCP server name — to the
// canonical engine slug stamped on a "db" span. The slug is a lowercase product
// name rather than whatever executable was typed, because it doubles as the
// brand-icon key the trace UI resolves: postgres / mysql / redis / mongodb have
// real logos there, and everything else falls back to a generic database glyph.
//
// The list is deliberately conservative. A name added here classifies every
// command that starts with it, so it must be a program whose ONLY job is
// talking to a datastore — which is why `docker`, `kubectl` and `supabase` are
// absent even though each can reach a database.
var dbEngines = map[string]string{
	// PostgreSQL
	"psql": "postgres", "pgcli": "postgres", "pg_dump": "postgres",
	"pg_dumpall": "postgres", "pg_restore": "postgres", "pg_isready": "postgres",
	"postgres": "postgres", "postgresql": "postgres", "pg": "postgres",
	// MySQL / MariaDB — one engine as far as the trace is concerned
	"mysql": "mysql", "mysqldump": "mysql", "mysqladmin": "mysql",
	"mysqlsh": "mysql", "mysqlimport": "mysql", "mycli": "mysql",
	"mariadb": "mysql", "mariadb-dump": "mysql",
	// Redis
	"redis-cli": "redis", "redis": "redis",
	// MongoDB
	"mongosh": "mongodb", "mongo": "mongodb", "mongodump": "mongodb",
	"mongorestore": "mongodb", "mongoexport": "mongodb", "mongoimport": "mongodb",
	"mongodb": "mongodb",
	// No brand logo — these render with the generic database glyph.
	"sqlite3": "sqlite", "sqlite": "sqlite", "litecli": "sqlite",
	"clickhouse-client": "clickhouse", "clickhouse": "clickhouse",
	"cqlsh": "cassandra", "cassandra": "cassandra",
	"influx": "influxdb", "influxdb": "influxdb",
	"duckdb": "duckdb",
	"sqlcmd": "mssql", "mssql-cli": "mssql", "mssql": "mssql", "sqlserver": "mssql",
	"cypher-shell": "neo4j", "neo4j": "neo4j",
	"snowsql": "snowflake", "snowflake": "snowflake",
	"bq": "bigquery", "bigquery": "bigquery",
}

// shellTools are the tool names, lowercased, whose `command` input is a shell
// string worth classifying. "bash" is what the Claude adapter emits and
// "shell" what the Codex parser hardcodes; the rest are the names other CLIs
// give the same tool and are here so a datastore call does not stop being one
// when the routine switches adapter. A shell tool NOT listed here loses only
// the db classification — its command still lands in the span detail, and the
// span still renders under its name-derived kind.
var shellTools = map[string]bool{
	"bash":              true,
	"shell":             true,
	"run_shell_command": true,
	"run_terminal_cmd":  true,
	"execute_command":   true,
}

// shellWrappers are leading tokens that prefix a command without being the
// command — a privilege or environment wrapper. Only their BARE forms are
// skipped: the moment a flag appears (`sudo -u postgres psql`) shellExecutable
// gives up, because following flags means implementing each wrapper's own
// option grammar for the sake of a nicer icon.
var shellWrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true,
	"command": true, "exec": true, "nohup": true, "time": true,
}

// shellExecutable returns the lowercased basename of the program a shell
// command invokes, or "" when it cannot be identified.
//
// The parse is deliberately shallow, and this is where the line is drawn:
// leading `VAR=value` assignments and bare wrappers are skipped, surrounding
// quotes and any directory prefix are stripped, and the FIRST remaining token
// wins. It does not split on `&&`, `;`, `|`, and does not enter subshells,
// here-docs or `-c` payloads. Doing that correctly means implementing shell
// quoting, and getting it wrong is exactly how `echo "psql is great"` turns
// into a database span — a lie in the trace. Everything the shallow parse
// misses (`cd /app && psql …`) merely under-classifies to the tool's own kind,
// which the "db" kind is built to degrade into.
func shellExecutable(cmd string) string {
	for _, tok := range strings.Fields(cmd) {
		tok = strings.Trim(tok, `"'`)
		if tok == "" {
			continue
		}
		// A flag means the tokens ran out before the program did (the `-u` of
		// `sudo -u postgres psql`). Stop rather than guess.
		if strings.HasPrefix(tok, "-") {
			return ""
		}
		if isEnvAssignment(tok) {
			continue
		}
		base := strings.ToLower(tok)
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if shellWrappers[base] {
			continue
		}
		return base
	}
	return ""
}

// isEnvAssignment reports whether tok is a leading `NAME=value` env prefix.
// The name must be a shell identifier, so `--flag=x` and `a/b=c` are not.
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := tok[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// dbEngineForCall names the datastore a tool call targets ("postgres",
// "redis", …), or "" when the call is not a database action. It is the only
// input-aware part of span classification.
//
// A call is promoted to kind "db" exactly when this can NAME the engine.
// Datastore work we cannot name — a `cockroach sql`, an MCP server called
// `prod-db` — deliberately keeps the kind its tool name gives it. That is the
// fallthrough contract DeriveSpanKind has always had: an unrecognised database
// CLI must degrade to "bash", never disappear, because a classifier that
// silently swallows a span is worse than one that under-classifies.
func dbEngineForCall(tool string, input map[string]any) string {
	if strings.HasPrefix(tool, "mcp__") {
		return dbEngineForMCPServer(mcpServerName(tool))
	}
	if !shellTools[strings.ToLower(tool)] {
		return ""
	}
	// A `command` on a tool we know execs a shell IS a shell string. The gate
	// is the tool name rather than the mere presence of a `command` key,
	// because reading an arbitrary tool's args as shell is how a non-shell
	// tool would get mislabelled a database call.
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return ""
	}
	return dbEngines[shellExecutable(cmd)]
}

// dbEngineForMCPServer resolves an MCP server name to a datastore engine. The
// whole name is matched first, then its `-`/`_`/`.`-separated segments, so the
// common `postgres-mcp` / `mcp-redis` server naming resolves. Matching a
// segment does mean a server called `postgres-notifier` reads as Postgres —
// accepted: the operator named it after the datastore.
func dbEngineForMCPServer(server string) string {
	if server == "" {
		return ""
	}
	server = strings.ToLower(server)
	if engine, ok := dbEngines[server]; ok {
		return engine
	}
	for _, seg := range strings.FieldsFunc(server, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if engine, ok := dbEngines[seg]; ok {
			return engine
		}
	}
	return ""
}

// mcpServerName extracts the <server> of mcp__<server>__<tool>, or "" when the
// name does not carry both halves.
func mcpServerName(tool string) string {
	parts := strings.Split(tool, "__")
	if len(parts) < 3 {
		return ""
	}
	return parts[1]
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
	case "db":
		// The engine IS the concrete tool for a datastore call — a psql span
		// tagged "Bash" renders the GNU bash logo and reads as a shell call in
		// the step header, which is the confusion the "db" kind exists to end.
		// The harness tool name is not lost: it stays the span's Name.
		if engine := dbEngineForCall(tool, input); engine != "" {
			attrs["tool"] = engine
		}
	}
	return attrs
}

// truncateBytes bounds s at max bytes on a rune boundary and appends a marker.
// Returns (result, wasTruncated). Shared by detail, input, and output capping.
func truncateBytes(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && cut > max-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)", true
}

// truncateDetail bounds the span detail at RunAgentSpanDetailMaxBytes.
func truncateDetail(s string) (string, bool) {
	return truncateBytes(s, RunAgentSpanDetailMaxBytes)
}

// captureInput marshals a tool's full input map to JSON, scrubs secrets, and
// caps it. Returns ("", false) for an empty map (a bare "{}" is noise) or a
// marshal failure — losing the args view must never break span capture.
func captureInput(input map[string]any) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", false
	}
	// Scrub AFTER marshalling: a token in an arg value would otherwise be
	// persisted verbatim (wrapScrubHandler only scrubs event.Content, never
	// the tool_call input map). Redaction may break strict JSON validity —
	// acceptable: the FE renders it best-effort as text when it can't parse.
	scrubbed := spanDetailScrubber.Scrub(string(raw))
	return truncateBytes(scrubbed, RunAgentSpanInputMaxBytes)
}

// captureOutput scrubs + caps a tool_result body for the Output field. The
// stream scrubber already redacted event.Content upstream; re-scrubbing here
// keeps the recorder self-contained (and safe when driven from a raw stream in
// tests). Idempotent — a second pass over already-redacted text is a no-op.
func captureOutput(content string) (string, bool) {
	if content == "" {
		return "", false
	}
	return truncateBytes(spanDetailScrubber.Scrub(content), RunAgentSpanOutputMaxBytes)
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

		input := p.input
		if input == nil {
			input = map[string]any{}
		}
		kind := DeriveSpanKind(p.name, input)
		detail, dTrunc := truncateDetail(deriveSpanDetail(p.name, input))
		inputJSON, iTrunc := captureInput(p.input)
		outputTail, oTrunc := captureOutput(ev.Content)
		// Count truncation once per span (not per field): the metric answers
		// "how many spans had something shortened", so a long command whose
		// detail AND input both truncate is one bounded span, not two.
		if dTrunc || iTrunc || oTrunc {
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
			RunID:           r.runID,
			StepID:          r.stepID,
			Seq:             r.seq,
			Kind:            kind,
			Name:            deriveSpanName(p.name),
			Detail:          detail,
			StartedAt:       p.startedAt,
			DurationMs:      dur,
			Status:          status,
			Attributes:      deriveSpanAttributes(p.name, kind, r.model, input),
			Input:           inputJSON,
			Output:          outputTail,
			InputTruncated:  iTrunc,
			OutputTruncated: oTrunc,
		}
		r.seq++
		r.sink(span)
	}
}
