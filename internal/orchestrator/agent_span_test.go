package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveSpanKind(t *testing.T) {
	cases := map[string]string{
		"Bash":                                 "bash",
		"Write":                                "write",
		"Edit":                                 "edit",
		"MultiEdit":                            "edit",
		"Read":                                 "read",
		"Grep":                                 "read",
		"Glob":                                 "read",
		"WebFetch":                             "http",
		"WebSearch":                            "http",
		"mcp__crewship-routines__save_routine": "mcp_tool",
		"SomethingUnknown":                     "tool",
	}
	for tool, want := range cases {
		if got := DeriveSpanKind(tool, nil); got != want {
			t.Errorf("DeriveSpanKind(%q, nil) = %q, want %q", tool, got, want)
		}
	}
}

// TestDeriveSpanKind_DatabaseCalls pins the "db" kind (#848 pillar 2.4): a
// datastore CLI must not render as an anonymous `bash` span next to an `ls`.
// The negative half matters as much as the positive one — the classifier reads
// a shell string, so anything that merely MENTIONS psql must stay `bash`, and
// an unrecognised datastore CLI must degrade to `bash` rather than vanish.
func TestDeriveSpanKind_DatabaseCalls(t *testing.T) {
	cases := []struct {
		name string
		tool string
		cmd  string
		want string
	}{
		// ── the point of the feature ──────────────────────────────────
		{"psql", "Bash", `psql -c "select 1" mydb`, "db"},
		{"redis-cli", "Bash", "redis-cli SET k v", "db"},
		{"mysql", "Bash", "mysql -u root -e 'show tables'", "db"},
		{"mongosh", "Bash", `mongosh --eval "db.users.find()"`, "db"},
		{"pg_dump piped", "Bash", "pg_dump mydb | gzip > dump.sql.gz", "db"},
		{"sqlite3", "Bash", "sqlite3 app.db .tables", "db"},
		{"absolute path", "Bash", "/usr/bin/redis-cli PING", "db"},
		{"env assignment prefix", "Bash", `PGPASSWORD=hunter2 psql -h db -c "select 1"`, "db"},
		{"two env assignments", "Bash", "PGHOST=db PGPORT=5432 psql -l", "db"},
		{"bare sudo wrapper", "Bash", "sudo psql -l", "db"},
		{"quoted executable", "Bash", `"psql" -l`, "db"},
		{"leading whitespace", "Bash", "   psql -l", "db"},
		{"codex shell tool", "shell", "psql -c 'select 1'", "db"},

		// ── plain shell work stays plain ──────────────────────────────
		{"ls", "Bash", "ls -la", "bash"},
		{"echo mentioning psql", "Bash", `echo "psql is great"`, "bash"},
		{"grep for psql", "Bash", "grep psql history.txt", "bash"},
		{"ls of a db binary", "Bash", "ls -l /usr/bin/mysqldump", "bash"},
		{"which psql", "Bash", "which psql", "bash"},
		{"cat a .sql file", "Bash", "cat migrations/001_redis-cli.sql", "bash"},
		{"empty command", "Bash", "", "bash"},
		{"flag-only command", "Bash", "--version", "bash"},

		// ── documented under-classification (degrade, never drop) ─────
		{"unknown db cli", "Bash", "cockroach sql -e 'select 1'", "bash"},
		{"sudo with flags", "Bash", "sudo -u postgres psql -l", "bash"},
		{"psql after &&", "Bash", "cd /app && psql -c 'select 1'", "bash"},

		// ── a command arg on a non-shell tool is not a shell string ───
		{"non-bash tool", "Write", "psql -l", "write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"command": tc.cmd}
			if got := DeriveSpanKind(tc.tool, in); got != tc.want {
				t.Errorf("DeriveSpanKind(%q, {command: %q}) = %q, want %q", tc.tool, tc.cmd, got, tc.want)
			}
		})
	}
}

// TestDeriveSpanKind_DatabaseMCPServers covers the other half of pillar 2.4:
// an MCP server that IS a datastore reads as "db", while every other MCP tool
// keeps the "mcp_tool" kind it has always had.
func TestDeriveSpanKind_DatabaseMCPServers(t *testing.T) {
	cases := map[string]string{
		"mcp__postgres__query":                 "db",
		"mcp__redis__get":                      "db",
		"mcp__postgres-mcp__list_tables":       "db",
		"mcp__mongodb__aggregate":              "db",
		"mcp__crewship-routines__save_routine": "mcp_tool",
		"mcp__slack__post_message":             "mcp_tool",
		"mcp__prod-db__query":                  "mcp_tool", // datastore we cannot name
		"mcp__short":                           "mcp_tool",
	}
	for tool, want := range cases {
		if got := DeriveSpanKind(tool, nil); got != want {
			t.Errorf("DeriveSpanKind(%q, nil) = %q, want %q", tool, got, want)
		}
	}
}

// TestDeriveSpanAttributes_DBEngine — a db span names its engine in
// `attributes.tool` so the trace renders the Postgres elephant / Redis cube
// instead of the GNU bash logo the harness tool name would resolve to.
func TestDeriveSpanAttributes_DBEngine(t *testing.T) {
	cases := []struct {
		tool, cmd, wantTool string
	}{
		{"Bash", `psql -c "select 1"`, "postgres"},
		{"Bash", "redis-cli PING", "redis"},
		{"Bash", "mysqldump mydb", "mysql"},
		{"Bash", "clickhouse-client --query 'select 1'", "clickhouse"},
		{"Bash", "ls -la", "Bash"}, // untouched for non-db spans
	}
	for _, tc := range cases {
		in := map[string]any{"command": tc.cmd}
		kind := DeriveSpanKind(tc.tool, in)
		attrs := deriveSpanAttributes(tc.tool, kind, "", in)
		if attrs["tool"] != tc.wantTool {
			t.Errorf("attributes[tool] for %q = %q, want %q", tc.cmd, attrs["tool"], tc.wantTool)
		}
	}

	attrs := deriveSpanAttributes("mcp__postgres__query", DeriveSpanKind("mcp__postgres__query", nil), "", map[string]any{})
	if attrs["tool"] != "postgres" {
		t.Errorf("attributes[tool] for a postgres MCP call = %q, want %q", attrs["tool"], "postgres")
	}
}

// TestAgentSpanRecorder_DatabaseSpan drives the classifier through the real
// event path — a Bash tool_call whose command is a psql invocation must reach
// the sink as a "db" span with its detail and engine intact.
func TestAgentSpanRecorder_DatabaseSpan(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_db", "step_db", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": `psql -c "insert into audit values (1)"`}, t0))
	rec.Observe(makeToolResult("t1", false, t0.Add(120*time.Millisecond)))
	rec.Observe(makeToolCall("t2", "Bash", map[string]any{"command": "ls -la /tmp"}, t0))
	rec.Observe(makeToolResult("t2", false, t0.Add(130*time.Millisecond)))

	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2", len(got))
	}
	if got[0].Kind != "db" {
		t.Errorf("psql span kind = %q, want %q", got[0].Kind, "db")
	}
	if got[0].Name != "Bash" {
		t.Errorf("psql span name = %q, want %q — the harness tool is still the name", got[0].Name, "Bash")
	}
	if got[0].Attributes["tool"] != "postgres" {
		t.Errorf("psql span attributes[tool] = %q, want %q", got[0].Attributes["tool"], "postgres")
	}
	if !strings.Contains(got[0].Detail, "insert into audit") {
		t.Errorf("psql span detail = %q, want the command", got[0].Detail)
	}
	if got[1].Kind != "bash" {
		t.Errorf("ls span kind = %q, want %q", got[1].Kind, "bash")
	}
}

// makeToolCall / makeToolResult build the AgentEvents the Claude adapter emits
// so the recorder test exercises the exact metadata shape the live parser
// produces (see parseClaudeCodeStreamJSON / emitToolResultBlock).
func makeToolCall(toolID, name string, input map[string]any, ts time.Time) AgentEvent {
	return AgentEvent{
		Type:    "tool_call",
		Content: name,
		Metadata: map[string]interface{}{
			"tool_name": name,
			"tool_id":   toolID,
			"input":     input,
		},
		Timestamp: ts,
	}
}

func makeToolResult(toolUseID string, isError bool, ts time.Time) AgentEvent {
	meta := map[string]interface{}{"tool_use_id": toolUseID}
	if isError {
		meta["is_error"] = true
	}
	return AgentEvent{Type: "tool_result", Content: "ok", Metadata: meta, Timestamp: ts}
}

func TestAgentSpanRecorder_MapsToolsToSpans(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })

	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// A Bash call.
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": "ls -la"}, t0))
	rec.Observe(makeToolResult("t1", false, t0.Add(150*time.Millisecond)))

	// A Write call (artifact_path attribute).
	rec.Observe(makeToolCall("t2", "Write", map[string]any{"file_path": "/output/report.md", "content": "x"}, t0.Add(time.Second)))
	rec.Observe(makeToolResult("t2", false, t0.Add(time.Second+50*time.Millisecond)))

	// An MCP tool call that errors.
	rec.Observe(makeToolCall("t3", "mcp__crewship-routines__save_routine", map[string]any{"slug": "demo"}, t0.Add(2*time.Second)))
	rec.Observe(makeToolResult("t3", true, t0.Add(2*time.Second+10*time.Millisecond)))

	if len(got) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(got))
	}

	// Ordering + identity.
	for i, s := range got {
		if s.RunID != "run_1" || s.StepID != "step_a" {
			t.Errorf("span %d: run/step = %q/%q", i, s.RunID, s.StepID)
		}
		if s.Seq != i {
			t.Errorf("span %d: seq = %d, want %d", i, s.Seq, i)
		}
	}

	// Bash span.
	if got[0].Kind != "bash" || got[0].Name != "Bash" || got[0].Detail != "ls -la" {
		t.Errorf("bash span = %+v", got[0])
	}
	if got[0].DurationMs != 150 {
		t.Errorf("bash duration = %d, want 150", got[0].DurationMs)
	}
	if got[0].Status != "ok" {
		t.Errorf("bash status = %q, want ok", got[0].Status)
	}

	// Write span carries artifact_path.
	if got[1].Kind != "write" {
		t.Errorf("write kind = %q", got[1].Kind)
	}
	if got[1].Attributes["artifact_path"] != "/output/report.md" {
		t.Errorf("write artifact_path = %q", got[1].Attributes["artifact_path"])
	}

	// MCP span: kind=mcp_tool, short name, error status.
	if got[2].Kind != "mcp_tool" || got[2].Name != "save_routine" {
		t.Errorf("mcp span kind/name = %q/%q", got[2].Kind, got[2].Name)
	}
	if got[2].Status != "error" {
		t.Errorf("mcp span status = %q, want error", got[2].Status)
	}
	if got[2].Attributes["tool"] != "mcp__crewship-routines__save_routine" {
		t.Errorf("mcp span tool attr = %q", got[2].Attributes["tool"])
	}
}

func TestAgentSpanRecorder_CapturesModelFromSystemInit(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	rec.Observe(AgentEvent{Type: "system", Content: "init", Metadata: map[string]interface{}{"model": "claude-opus-4-8"}, Timestamp: t0})
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": "echo hi"}, t0))
	rec.Observe(makeToolResult("t1", false, t0.Add(time.Millisecond)))

	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d", len(got))
	}
	if got[0].Attributes["model"] != "claude-opus-4-8" {
		t.Errorf("model attr = %q, want claude-opus-4-8", got[0].Attributes["model"])
	}
}

func TestAgentSpanRecorder_PerStepCapAndDetailTruncation(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Detail truncation: a command longer than the cap.
	longCmd := strings.Repeat("a", RunAgentSpanDetailMaxBytes+500)
	rec.Observe(makeToolCall("big", "Bash", map[string]any{"command": longCmd}, t0))
	rec.Observe(makeToolResult("big", false, t0.Add(time.Millisecond)))
	if len(got[0].Detail) > RunAgentSpanDetailMaxBytes+len("...(truncated)") {
		t.Errorf("detail not truncated: len=%d", len(got[0].Detail))
	}
	if !strings.HasSuffix(got[0].Detail, "...(truncated)") {
		t.Errorf("detail missing truncation marker: %q", got[0].Detail[len(got[0].Detail)-20:])
	}
	if rec.Truncated() != 1 {
		t.Errorf("Truncated() = %d, want 1", rec.Truncated())
	}

	// Per-step cap: feed far more than the cap; only cap spans are sunk.
	for i := 0; i < RunAgentSpanMaxPerStep+50; i++ {
		id := "x" + strings.Repeat("y", i%5) + time.Duration(i).String()
		rec.Observe(makeToolCall(id, "Read", map[string]any{"file_path": "/f"}, t0))
		rec.Observe(makeToolResult(id, false, t0.Add(time.Millisecond)))
	}
	if len(got) != RunAgentSpanMaxPerStep {
		t.Errorf("sunk %d spans, want cap %d", len(got), RunAgentSpanMaxPerStep)
	}
	if rec.Dropped() == 0 {
		t.Errorf("expected Dropped() > 0 after exceeding cap")
	}
}

// toolResultWith builds a tool_result event carrying a specific output body so
// the Output-capture path can be exercised (makeToolResult hard-codes "ok").
func toolResultWith(toolUseID, content string, ts time.Time) AgentEvent {
	return AgentEvent{
		Type:      "tool_result",
		Content:   content,
		Metadata:  map[string]interface{}{"tool_use_id": toolUseID},
		Timestamp: ts,
	}
}

func TestAgentSpanRecorder_CapturesOutputAndInput(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// The whole point of #847: you can see WHAT the agent ran (input args) and
	// WHAT it returned (output), not just the one-field command detail.
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{
		"command":     "python3 parse_vypis.py",
		"description": "parse the statement",
	}, t0))
	rec.Observe(toolResultWith("t1", "parsed 42 rows\nwrote out.json", t0.Add(1200*time.Millisecond)))

	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d", len(got))
	}
	s := got[0]
	if s.Output != "parsed 42 rows\nwrote out.json" {
		t.Errorf("Output = %q, want the tool_result body", s.Output)
	}
	// Input is the FULL input map as JSON — the "args" acceptance asks for,
	// carrying both command and description (detail only carries `command`).
	if !strings.Contains(s.Input, "python3 parse_vypis.py") || !strings.Contains(s.Input, "parse the statement") {
		t.Errorf("Input = %q, want full input JSON (command + description)", s.Input)
	}
	// A tool with no input args leaves Input empty rather than a bare "{}".
	rec.Observe(makeToolCall("t2", "Read", map[string]any{}, t0.Add(2*time.Second)))
	rec.Observe(toolResultWith("t2", "file body", t0.Add(2*time.Second+time.Millisecond)))
	if got[1].Input != "" {
		t.Errorf("empty-input span Input = %q, want empty", got[1].Input)
	}
}

func TestAgentSpanRecorder_OutputInputScrubbedAndTruncated(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Defense-in-depth: even though the stream scrubber runs upstream, the
	// recorder re-scrubs input args + output before they are persisted, so a
	// leaked token never lands verbatim in the journal.
	// Assembled from fragments so the full token literal never lands in source
	// (keeps the repo secret scanner quiet); at runtime it's a valid ghp_ shape
	// the scrubber matches.
	secret := "ghp_" + strings.Repeat("a", 12) + "1234567890"
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": "curl -H 'auth: " + secret + "'"}, t0))
	rec.Observe(toolResultWith("t1", "server said token "+secret+" ok", t0.Add(time.Millisecond)))
	if strings.Contains(got[0].Output, secret) {
		t.Errorf("Output leaked secret: %q", got[0].Output)
	}
	if strings.Contains(got[0].Input, secret) {
		t.Errorf("Input leaked secret: %q", got[0].Input)
	}

	// A tool_result larger than the output cap is bounded, marked, AND flags
	// the span so the UI can show a "truncated — see step Output" chip.
	bigOut := strings.Repeat("z", RunAgentSpanOutputMaxBytes+500)
	rec.Observe(makeToolCall("t2", "Read", map[string]any{"file_path": "/big"}, t0))
	rec.Observe(toolResultWith("t2", bigOut, t0.Add(time.Millisecond)))
	if len(got[1].Output) > RunAgentSpanOutputMaxBytes+len("...(truncated)") {
		t.Errorf("Output not truncated: len=%d", len(got[1].Output))
	}
	if !strings.HasSuffix(got[1].Output, "...(truncated)") {
		t.Errorf("Output missing truncation marker")
	}
	if !got[1].OutputTruncated {
		t.Errorf("OutputTruncated flag not set on a truncated span")
	}

	// The output cap is deliberately roomy (a strict-JSON deliverable — a
	// month of transactions — must survive whole): an 8 KB result fits under
	// the cap and is NOT truncated, where the old 2 KB cap would have cut it.
	midOut := strings.Repeat("j", 8*1024)
	if 8*1024 >= RunAgentSpanOutputMaxBytes {
		t.Fatalf("test assumes output cap > 8 KB, got %d", RunAgentSpanOutputMaxBytes)
	}
	rec.Observe(makeToolCall("t3", "Bash", map[string]any{"command": "python3 parse.py"}, t0))
	rec.Observe(toolResultWith("t3", midOut, t0.Add(time.Millisecond)))
	if got[2].OutputTruncated || strings.HasSuffix(got[2].Output, "...(truncated)") {
		t.Errorf("8 KB output should fit under the cap, got truncated (len=%d)", len(got[2].Output))
	}

	// Input keeps the tighter 2 KB cap and flags its own truncation.
	bigCmd := strings.Repeat("x", RunAgentSpanInputMaxBytes+500)
	rec.Observe(makeToolCall("t4", "Bash", map[string]any{"command": bigCmd}, t0))
	rec.Observe(toolResultWith("t4", "ok", t0.Add(time.Millisecond)))
	if !got[3].InputTruncated {
		t.Errorf("InputTruncated flag not set on a truncated-input span")
	}
	if !strings.HasSuffix(got[3].Input, "...(truncated)") {
		t.Errorf("Input missing truncation marker")
	}
}

func TestAgentSpanRecorder_NoToolCallsNoSpans(t *testing.T) {
	called := false
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { called = true })
	t0 := time.Now()
	// Only text/thinking/result events — no tool_use pairs.
	rec.Observe(AgentEvent{Type: "text", Content: "hello", Timestamp: t0})
	rec.Observe(AgentEvent{Type: "thinking", Content: "...", Timestamp: t0})
	rec.Observe(AgentEvent{Type: "result", Content: "done", Metadata: map[string]interface{}{"total_cost_usd": 0.01}, Timestamp: t0})
	// An unmatched tool_result (no preceding tool_call) must not produce a span.
	rec.Observe(makeToolResult("orphan", false, t0))
	if called {
		t.Errorf("sink invoked with no completed tool_use pairs")
	}
}
