package orchestrator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logcollector"
)

// readLogEntries reads back the JSONL log file the OutputBuffer wrote for the
// given crew/agent under base.
func readLogEntries(t *testing.T, base, crewID, agentID string) []logcollector.LogEntry {
	t.Helper()
	path := filepath.Join(base, "crews", crewID, "agents", agentID, "current.jsonl")
	f, err := os.Open(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	var out []logcollector.LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e logcollector.LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestNewBufferingHandler_BuffersAccumulatesAndCaptures(t *testing.T) {
	base := t.TempDir()
	w := logcollector.NewWriter(base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logBuf := logcollector.NewOutputBuffer(w, "crew1", "agent1")

	handler, acc := NewBufferingHandler(BufferingHandlerOpts{
		LogBuf:            logBuf,
		AgentSlug:         "agent1",
		AccumulateText:    true,
		CaptureResultMeta: true,
	})

	ts := time.Now().UTC()
	// "text" events accumulate into acc.Text(); they are streamed events so
	// the buffer aggregates them — newline forces a flush so we can read them.
	handler(AgentEvent{Type: "text", Content: "Hello ", Timestamp: ts})
	handler(AgentEvent{Type: "text", Content: "world\n", Timestamp: ts})
	// A non-streamed event flushes immediately.
	handler(AgentEvent{Type: "tool_call", Content: "ls", Timestamp: ts})
	// "result" carries the run metadata we want captured.
	resultMeta := map[string]any{
		"total_cost_usd": 0.42,
		"usage": map[string]any{
			"input_tokens":  float64(100),
			"output_tokens": float64(50),
		},
	}
	handler(AgentEvent{Type: "result", Content: "", Metadata: resultMeta, Timestamp: ts})

	logBuf.Close()
	w.Close()

	if got, want := acc.Text(), "Hello world\n"; got != want {
		t.Errorf("acc.Text() = %q, want %q", got, want)
	}
	rm := acc.ResultMeta()
	if rm == nil {
		t.Fatalf("acc.ResultMeta() = nil, want captured map")
	}
	if rm["total_cost_usd"] != 0.42 {
		t.Errorf("captured total_cost_usd = %v, want 0.42", rm["total_cost_usd"])
	}

	entries := readLogEntries(t, base, "crew1", "agent1")
	// Expect: aggregated text line, tool_call, result → 3 entries.
	if len(entries) != 3 {
		t.Fatalf("got %d log entries, want 3: %+v", len(entries), entries)
	}
	events := map[string]bool{}
	for _, e := range entries {
		events[e.Event] = true
		if e.Agent != "agent1" {
			t.Errorf("entry.Agent = %q, want agent1", e.Agent)
		}
		if e.Level != "info" {
			t.Errorf("entry.Level = %q, want info", e.Level)
		}
	}
	for _, want := range []string{"text", "tool_call", "result"} {
		if !events[want] {
			t.Errorf("missing log entry for event %q", want)
		}
	}
}

func TestNewBufferingHandler_DisabledOptions(t *testing.T) {
	// With accumulation/capture off and a nil buffer, the handler is a no-op
	// that never panics and leaves the accumulator empty.
	handler, acc := NewBufferingHandler(BufferingHandlerOpts{AgentSlug: "agent1"})
	handler(AgentEvent{Type: "text", Content: "ignored", Timestamp: time.Now()})
	handler(AgentEvent{Type: "result", Content: "", Metadata: map[string]any{"x": 1}, Timestamp: time.Now()})
	if acc.Text() != "" {
		t.Errorf("acc.Text() = %q, want empty", acc.Text())
	}
	if acc.ResultMeta() != nil {
		t.Errorf("acc.ResultMeta() = %v, want nil", acc.ResultMeta())
	}
}

func TestNewBufferingHandler_OnLogError(t *testing.T) {
	// An invalid agent ID makes the underlying Writer.Append fail, which must
	// surface through OnLogError.
	base := t.TempDir()
	w := logcollector.NewWriter(base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logBuf := logcollector.NewOutputBuffer(w, "crew1", "bad/agent")
	defer logBuf.Close()

	var gotErr error
	handler, _ := NewBufferingHandler(BufferingHandlerOpts{
		LogBuf:     logBuf,
		AgentSlug:  "bad/agent",
		OnLogError: func(err error) { gotErr = err },
	})
	// tool_call is non-streamed → flushes immediately → Append runs now.
	handler(AgentEvent{Type: "tool_call", Content: "x", Timestamp: time.Now()})
	if gotErr == nil {
		t.Fatalf("OnLogError was not invoked for an invalid agent ID")
	}
}

func TestParseResultUsage(t *testing.T) {
	tests := []struct {
		name     string
		meta     any
		wantCost float64
		wantIn   int
		wantOut  int
	}{
		{
			name: "well-formed",
			meta: map[string]any{
				"total_cost_usd": 1.25,
				"usage": map[string]any{
					"input_tokens":  float64(300),
					"output_tokens": float64(120),
				},
			},
			wantCost: 1.25, wantIn: 300, wantOut: 120,
		},
		{
			name:     "missing fields",
			meta:     map[string]any{"num_turns": float64(3)},
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name: "wrong types",
			meta: map[string]any{
				"total_cost_usd": "1.25", // string, not float64
				"usage": map[string]any{
					"input_tokens":  "300",
					"output_tokens": true,
				},
			},
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "usage not a map",
			meta:     map[string]any{"usage": "nope", "total_cost_usd": 0.5},
			wantCost: 0.5, wantIn: 0, wantOut: 0,
		},
		{
			name:     "nil meta",
			meta:     nil,
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "not a map",
			meta:     "not a map",
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "typed nil map",
			meta:     map[string]any(nil),
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cost, in, out := ParseResultUsage(tc.meta)
			if cost != tc.wantCost || in != tc.wantIn || out != tc.wantOut {
				t.Errorf("ParseResultUsage = (%v,%d,%d), want (%v,%d,%d)",
					cost, in, out, tc.wantCost, tc.wantIn, tc.wantOut)
			}
		})
	}
}

func TestNewBufferingHandler_SessionInitCapture(t *testing.T) {
	initMeta := func(sessionID string) map[string]any {
		return map[string]any{
			"subtype":             "init",
			"model":               "claude-opus-4-8",
			"session_id":          sessionID,
			"claude_code_version": "2.1.219",
		}
	}

	tests := []struct {
		name          string
		capture       bool
		events        []AgentEvent
		wantSession   string // "" means SessionInit() must be nil
		wantResolved  string
		wantNilResult bool
	}{
		{
			name:         "first init wins",
			capture:      true,
			events:       []AgentEvent{{Type: "system", Metadata: initMeta("sess-1")}, {Type: "system", Metadata: initMeta("sess-2")}},
			wantSession:  "sess-1",
			wantResolved: "claude-opus-4-8",
		},
		{
			name:    "non-init system events do not claim the slot",
			capture: true,
			events: []AgentEvent{
				{Type: "system", Metadata: map[string]any{"subtype": "api_retry", "attempt": 1}},
				{Type: "system", Metadata: initMeta("sess-1")},
			},
			wantSession:  "sess-1",
			wantResolved: "claude-opus-4-8",
		},
		{
			name:        "no system event at all (non-Claude adapter)",
			capture:     true,
			events:      []AgentEvent{{Type: "text", Content: "hi"}},
			wantSession: "",
		},
		{
			name:        "capture disabled",
			capture:     false,
			events:      []AgentEvent{{Type: "system", Metadata: initMeta("sess-1")}},
			wantSession: "",
		},
		{
			name:        "metadata is not a map",
			capture:     true,
			events:      []AgentEvent{{Type: "system", Metadata: "init"}},
			wantSession: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, acc := NewBufferingHandler(BufferingHandlerOpts{CaptureResultMeta: tc.capture})
			for _, ev := range tc.events {
				handler(ev)
			}
			got := acc.SessionInit()
			if tc.wantSession == "" {
				if got != nil {
					t.Fatalf("SessionInit() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("SessionInit() = nil, want the init event's metadata")
			}
			if got["session_id"] != tc.wantSession {
				t.Errorf("captured session_id = %v, want %q", got["session_id"], tc.wantSession)
			}
			if acc.ResolvedModel() != tc.wantResolved {
				t.Errorf("ResolvedModel() = %q, want %q", acc.ResolvedModel(), tc.wantResolved)
			}
		})
	}
}

func TestMergeSessionInitMeta(t *testing.T) {
	tests := []struct {
		name string
		meta any
		want map[string]any
	}{
		{
			name: "full provenance, session-scoped fields dropped",
			meta: map[string]any{
				"subtype":             "init",
				"claude_code_version": "2.1.219",
				"apiKeySource":        "ANTHROPIC_API_KEY",
				"permissionMode":      "bypassPermissions",
				"session_id":          "sess-1",
				"model":               "claude-opus-4-8",
				"cwd":                 "/workspace",
				"tools":               []string{"Bash"},
				"capabilities":        []string{"interrupt_receipt_v1"},
				"skills":              json.RawMessage(`[{"name":"x"}]`),
				"mcp_servers":         []json.RawMessage{json.RawMessage(`{"name":"memory"}`)},
			},
			want: map[string]any{
				"duration_ms":     int64(7),
				"cli_version":     "2.1.219",
				"api_key_source":  "ANTHROPIC_API_KEY",
				"permission_mode": "bypassPermissions",
				"session_id":      "sess-1",
			},
		},
		{
			name: "unrecognised apiKeySource never lands verbatim",
			meta: map[string]any{"apiKeySource": "/home/agent/.claude/creds.json"},
			want: map[string]any{"duration_ms": int64(7), "api_key_source": "other"},
		},
		{
			name: "non-string apiKeySource is dropped, not stored",
			meta: map[string]any{"apiKeySource": map[string]any{"path": "/secret"}},
			want: map[string]any{"duration_ms": int64(7)},
		},
		{
			// Projected to name + category, never copied verbatim: see
			// TestMergeSessionInitMeta_DropsTheFreeTextMessage for why the
			// `message` field must not reach a hash-chained record.
			name: "mcp_server_errors projected when it reports something",
			meta: map[string]any{"mcp_server_errors": json.RawMessage(
				`[{"name":"memory","type":"invalid_config","message":"connect failed"}]`)},
			want: map[string]any{
				"duration_ms":            int64(7),
				"mcp_server_errors":      []map[string]any{{"name": "memory", "type": "invalid_config"}},
				"mcp_server_error_count": 1,
			},
		},
		{
			name: "empty mcp_server_errors stays absent",
			meta: map[string]any{"mcp_server_errors": json.RawMessage(`[]`)},
			want: map[string]any{"duration_ms": int64(7)},
		},
		{
			name: "null mcp_server_errors stays absent",
			meta: map[string]any{"mcp_server_errors": json.RawMessage(`null`)},
			want: map[string]any{"duration_ms": int64(7)},
		},
		{
			name: "absent keys stay absent",
			meta: map[string]any{"subtype": "init"},
			want: map[string]any{"duration_ms": int64(7)},
		},
		{
			name: "nil meta is a no-op",
			meta: nil,
			want: map[string]any{"duration_ms": int64(7)},
		},
		{
			name: "meta is not a map",
			meta: "init",
			want: map[string]any{"duration_ms": int64(7)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{"duration_ms": int64(7)}
			MergeSessionInitMeta(dst, tc.meta)
			if !reflect.DeepEqual(dst, tc.want) {
				t.Errorf("dst = %+v, want %+v", dst, tc.want)
			}
		})
	}
}

func TestMergeResultUsageMeta(t *testing.T) {
	t.Run("copies known keys, preserves dst", func(t *testing.T) {
		dst := map[string]any{"duration_ms": int64(1000)}
		meta := map[string]any{
			"total_cost_usd":     0.9,
			"num_turns":          float64(4),
			"usage":              map[string]any{"input_tokens": float64(10)},
			"model_usage":        map[string]any{"claude": 1},
			"permission_denials": json.RawMessage(`[{"tool_name":"Bash"}]`),
			"unrelated":          "drop me",
		}
		MergeResultUsageMeta(dst, meta)
		if dst["duration_ms"] != int64(1000) {
			t.Errorf("duration_ms clobbered: %v", dst["duration_ms"])
		}
		for _, k := range []string{"total_cost_usd", "num_turns", "usage", "model_usage", "permission_denials"} {
			if _, ok := dst[k]; !ok {
				t.Errorf("missing copied key %q", k)
			}
		}
		if _, ok := dst["unrelated"]; ok {
			t.Errorf("unrelated key should not be copied")
		}
	})

	t.Run("nil meta is a no-op", func(t *testing.T) {
		dst := map[string]any{"duration_ms": int64(5)}
		MergeResultUsageMeta(dst, nil)
		if len(dst) != 1 {
			t.Errorf("dst mutated by nil meta: %+v", dst)
		}
	})

	t.Run("absent keys skipped", func(t *testing.T) {
		dst := map[string]any{}
		MergeResultUsageMeta(dst, map[string]any{"total_cost_usd": 0.1})
		if len(dst) != 1 {
			t.Errorf("expected only present key copied, got %+v", dst)
		}
		// The adapter stamps permission_denials only when the CLI actually
		// refused something, so its absence here has to survive the merge —
		// an empty key on the run record would read as "denials, but none".
		if _, ok := dst["permission_denials"]; ok {
			t.Errorf("permission_denials must stay absent when the result event carried none: %+v", dst)
		}
	})
}

// A run's completed-meta becomes the payload of its `run.completed` journal
// entry, and journal entries are HMAC-chained and append-only — what lands
// there cannot be redacted later. The credential scrubber does not help: it
// rewrites an event's Content and never touches Metadata.
//
// mcp_server_errors[].message is free text the CLI produced while parsing a
// config file, so it is the one field on this envelope that can carry
// something we never chose to store. The server name and the error category
// are what an operator acts on; the message stays in the (bounded, live)
// output chunk instead.
func TestMergeSessionInitMeta_DropsTheFreeTextMessage(t *testing.T) {
	const secretish = "connect https://mcp.example.com?token=sk-live-9f3a failed"

	cases := []struct {
		name string
		in   any
	}{
		{"decoded maps", []map[string]any{
			{"name": "crewship-memory", "type": "invalid_config", "message": secretish},
		}},
		{"raw JSON array", json.RawMessage(
			`[{"name":"crewship-memory","type":"invalid_config","message":"` + secretish + `"}]`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{"mcp_server_errors": tc.in})

			blob, err := json.Marshal(dst)
			if err != nil {
				t.Fatalf("marshal run meta: %v", err)
			}
			if bytes.Contains(blob, []byte("sk-live-9f3a")) {
				t.Fatalf("free-text message reached the run record, which is hash-chained and cannot be redacted: %s", blob)
			}
			// The report itself must survive — dropping the message must not
			// turn a degraded run back into a silent one.
			if !bytes.Contains(blob, []byte("crewship-memory")) {
				t.Errorf("server name lost: %s", blob)
			}
			if !bytes.Contains(blob, []byte("invalid_config")) {
				t.Errorf("error category lost: %s", blob)
			}
		})
	}
}

// A permission denial names the tool AND carries the full tool input the CLI
// refused to run — a Bash command line, the body of a Write. That input is
// arbitrary agent-generated text, it reaches the run record through Metadata
// (which the scrubber never rewrites), and the run's terminal journal entry is
// HMAC-chained and append-only.
//
// So the denial is worth recording and its argument is not: "Bash was denied"
// is the diagnosis, `curl -H "Authorization: Bearer …"` is a secret we chose to
// keep forever. Same projection rule as mcp_server_errors, same reason.
func TestMergeResultUsageMeta_DropsDeniedToolInput(t *testing.T) {
	const secretish = `curl -H Authorization:Bearer-sk-live-9f3a https://api.example.com`

	cases := []struct {
		name string
		in   any
	}{
		{"raw JSON array", json.RawMessage(
			`[{"tool_name":"Bash","tool_use_id":"tu_1","tool_input":{"command":"` + secretish + `"}}]`)},
		{"decoded maps", []map[string]any{
			{"tool_name": "Bash", "tool_input": map[string]any{"command": secretish}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeResultUsageMeta(dst, map[string]any{"permission_denials": tc.in})

			blob, err := json.Marshal(dst)
			if err != nil {
				t.Fatalf("marshal run meta: %v", err)
			}
			if bytes.Contains(blob, []byte("sk-live-9f3a")) {
				t.Fatalf("denied tool input reached the run record, which is hash-chained and cannot be redacted: %s", blob)
			}
			// The denial itself must survive — dropping the argument must not
			// turn a blocked run back into one that merely chose not to act.
			if !bytes.Contains(blob, []byte("Bash")) {
				t.Errorf("the denied tool is not named: %s", blob)
			}
		})
	}
}

// Absence has to keep meaning "nothing was denied", or a gate over this key
// reads a permission-blocked run as a clean one.
func TestMergeResultUsageMeta_NoDenialsStaysAbsent(t *testing.T) {
	for _, empty := range []any{json.RawMessage(`[]`), json.RawMessage(`null`), []map[string]any{}, nil} {
		dst := map[string]any{}
		MergeResultUsageMeta(dst, map[string]any{"permission_denials": empty})
		if v, ok := dst["permission_denials"]; ok {
			t.Errorf("permission_denials = %v for input %v; absent must mean nothing was denied", v, empty)
		}
	}
}

// Mirror of the journal-side guard: a shape we do not recognise must not read
// as "nothing was skipped" on the run record either, because a gate keys off
// the absence of this key. The names may be lost; the fact must not be.
func TestMergeSessionInitMeta_UnrecognisedSkipShapeStillReports(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"object keyed by server name", `{"crewship-memory":{"type":"invalid_config"}}`},
		{"array of strings", `["crewship-memory"]`},
		{"objects with renamed keys", `[{"server":"crewship-memory","reason":"invalid_config"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{"mcp_server_errors": json.RawMessage(tc.raw)})

			v, ok := dst["mcp_server_errors"]
			if !ok {
				t.Fatalf("mcp_server_errors absent — a gate reads that as 'nothing was skipped', "+
					"but the CLI reported %s", tc.raw)
			}
			blob, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(blob, []byte(`{}`)) {
				t.Errorf("stored an empty skip object (%s) — an alarm that names nothing", blob)
			}
		})
	}
}

// storedDenial is the shape a projected permission_denials entry takes on the
// run record. Tests decode through JSON rather than asserting on the
// map[string]any directly, because JSON is what actually lands in the
// hash-chained journal payload.
type storedDenial struct {
	ToolName string `json:"tool_name"`
	Type     string `json:"type"`
	Count    int    `json:"count"`
}

func decodeStoredDenials(t *testing.T, v any) []storedDenial {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal permission_denials: %v", err)
	}
	var out []storedDenial
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("decode permission_denials %s: %v", blob, err)
	}
	return out
}

// denialsJSON builds a CLI-shaped permission_denials array from tool names, one
// element per name — the CLI reports one entry per refusal, not per tool.
func denialsJSON(names ...string) json.RawMessage {
	elems := make([]string, 0, len(names))
	for _, n := range names {
		elems = append(elems, `{"tool_name":"`+n+`","tool_input":{"command":"x"}}`)
	}
	return json.RawMessage("[" + strings.Join(elems, ",") + "]")
}

// permission_denials got the projection its sibling mcp_server_errors got, and
// none of the guards. Each gap below turns a run the CLI BLOCKED into a run
// that reads as one that chose not to act — the single misdiagnosis this field
// exists to prevent — or writes something untrue into a record that is
// hash-chained and cannot be corrected afterwards.
func TestMergeResultUsageMeta_DenialGuards(t *testing.T) {
	// One retry storm: the same tool refused over and over, with a second,
	// rarer tool denied late enough that a cap applied before deduping would
	// drop it.
	stormNames := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		if i == 34 {
			stormNames = append(stormNames, "WebFetch")
			continue
		}
		stormNames = append(stormNames, "Bash")
	}
	// Forty genuinely distinct tools: more than the list may keep, so the row
	// has to say it is partial.
	manyNames := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		manyNames = append(manyNames, fmt.Sprintf("Tool%02d", i))
	}

	cases := []struct {
		name  string
		in    any
		check func(t *testing.T, dst map[string]any)
	}{
		{
			// A release that reports denials as an object keyed by tool decodes
			// into no objects at all. Storing nothing lets the key's absence —
			// which every reader takes as "nothing was denied" — describe a run
			// that was blocked.
			name: "shape we cannot read keeps the alarm",
			in:   json.RawMessage(`{"Bash":{"tool_input":{"command":"rm -rf /"}}}`),
			check: func(t *testing.T, dst map[string]any) {
				v, ok := dst["permission_denials"]
				if !ok {
					t.Fatalf("permission_denials absent — a reader takes that as 'nothing was denied' "+
						"while the CLI reported a refusal: %+v", dst)
				}
				got := decodeStoredDenials(t, v)
				if len(got) != 1 || got[0].Type != "unrecognized_shape" {
					t.Fatalf("permission_denials = %+v, want the unrecognized_shape sentinel", got)
				}
				if got[0].ToolName != "" {
					t.Errorf("sentinel names a tool (%q) we never read", got[0].ToolName)
				}
				if blob, _ := json.Marshal(v); bytes.Contains(blob, []byte("rm -rf")) {
					t.Errorf("the refused tool input reached the run record: %s", blob)
				}
			},
		},
		{
			// Decodes as objects, but the element key was renamed: every entry
			// projects to {} and the length guard happily writes [{},{}] into a
			// permanent record while the CLI shows two named denials.
			name: "renamed element key never stores blanks",
			in:   json.RawMessage(`[{"tool":"Bash"},{"tool":"Write"}]`),
			check: func(t *testing.T, dst map[string]any) {
				v, ok := dst["permission_denials"]
				if !ok {
					t.Fatalf("permission_denials absent for a reported refusal: %+v", dst)
				}
				blob, _ := json.Marshal(v)
				if bytes.Contains(blob, []byte("{}")) {
					t.Fatalf("stored a blank denial (%s) — an alarm naming no tool, kept forever", blob)
				}
				got := decodeStoredDenials(t, v)
				if len(got) != 1 || got[0].Type != "unrecognized_shape" {
					t.Errorf("permission_denials = %+v, want the unrecognized_shape sentinel", got)
				}
			},
		},
		{
			name: "repeats collapse to the tool and keep the count",
			in:   denialsJSON(stormNames...),
			check: func(t *testing.T, dst map[string]any) {
				got := decodeStoredDenials(t, dst["permission_denials"])
				if len(got) != 2 {
					t.Fatalf("permission_denials = %+v, want one entry per denied TOOL (2)", got)
				}
				byTool := map[string]int{}
				for _, d := range got {
					byTool[d.ToolName] = d.Count
				}
				if byTool["Bash"] != 39 {
					t.Errorf("Bash count = %d, want 39 — deduping the name must not lose how hard it retried",
						byTool["Bash"])
				}
				if _, ok := byTool["WebFetch"]; !ok {
					t.Errorf("permission_denials = %+v: the tool denied on the 35th attempt is missing, "+
						"so the row says only Bash was blocked", got)
				}
				if _, ok := dst["permission_denials_truncated"]; ok {
					t.Errorf("marked truncated for two distinct tools: %+v", dst)
				}
			},
		},
		{
			name: "more distinct tools than the cap says so",
			in:   denialsJSON(manyNames...),
			check: func(t *testing.T, dst map[string]any) {
				got := decodeStoredDenials(t, dst["permission_denials"])
				if len(got) != sessionInitListMax {
					t.Fatalf("kept %d denials, want the %d-element cap", len(got), sessionInitListMax)
				}
				if dst["permission_denials_truncated"] != true {
					t.Errorf("no truncation marker: an operator reads %d tools as the whole story, "+
						"the way mcp_server_errors_truncated stops them doing", len(got))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeResultUsageMeta(dst, map[string]any{"permission_denials": tc.in})
			tc.check(t, dst)
		})
	}
}

// The journal entry bounds every CLI-supplied scalar it stores and says why:
// nothing downstream caps a journal payload's size — not the emitter, not the
// writer, not the column. The run record is the OTHER durable copy of the same
// fields and it copied them verbatim, so a CLI that reports a megabyte-long
// session id writes a megabyte into a hash-chained row that can never be
// rewritten.
func TestMergeSessionInitMeta_BoundsWhatItCopies(t *testing.T) {
	// Longer than the cap by three orders of magnitude, so a bounded value is
	// unmistakably bounded and not merely "shorter than the input".
	huge := strings.Repeat("A", 64*1024)

	cases := []struct {
		name   string
		src    string
		dst    string
		expect string // the value that must be stored, when it is knowable
	}{
		{name: "cli version", src: "claude_code_version", dst: "cli_version"},
		{name: "permission mode", src: "permissionMode", dst: "permission_mode"},
		{name: "session id", src: "session_id", dst: "session_id"},
		{name: "api key source", src: "apiKeySource", dst: "api_key_source", expect: "other"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{tc.src: huge})

			got, ok := dst[tc.dst].(string)
			if !ok {
				t.Fatalf("%s = %v, want a bounded string", tc.dst, dst[tc.dst])
			}
			if tc.expect != "" {
				if got != tc.expect {
					t.Fatalf("%s = %q, want %q", tc.dst, got, tc.expect)
				}
				return
			}
			if len(got) > sessionInitFieldMax+len("...(truncated)") {
				t.Errorf("%s stored %d bytes from a %d-byte CLI value; the journal path caps the same "+
					"field at %d and nothing downstream caps a payload at all",
					tc.dst, len(got), len(huge), sessionInitFieldMax)
			}
		})
	}
}

// A denial list the producer could only PARTLY read is the worst shape to store
// confidently: `run get` prints a "Tools denied" row naming some of the blocked
// tools and reads as complete. The sentinel exists for exactly this — it just
// never fired unless EVERY entry was unreadable.
func TestMergeResultUsageMeta_PartlyReadableDenialsSayTheListIsPartial(t *testing.T) {
	cases := []struct {
		name           string
		in             any
		wantTools      []string // tool names that must survive
		wantUnreadable int      // how many entries the sentinel must account for
	}{
		{
			// A release that renames the key on SOME entries: the readable ones
			// project fine, the rest vanish, and because one survived neither the
			// sentinel nor any marker was written.
			name:           "one entry renamed",
			in:             json.RawMessage(`[{"tool_name":"Bash"},{"tool":"Write"}]`),
			wantTools:      []string{"Bash"},
			wantUnreadable: 1,
		},
		{
			// A tool name that is no longer a string projects to nothing the same
			// way.
			name:           "non-string tool name",
			in:             json.RawMessage(`[{"tool_name":"Bash"},{"tool_name":{"id":"Write"}},{"tool_name":42}]`),
			wantTools:      []string{"Bash"},
			wantUnreadable: 2,
		},
		{
			// Every entry unreadable already produced a sentinel; it must keep
			// doing so, and now say how many refusals it stands for.
			name:           "every entry renamed",
			in:             json.RawMessage(`[{"tool":"Bash"},{"tool":"Write"}]`),
			wantTools:      nil,
			wantUnreadable: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeResultUsageMeta(dst, map[string]any{"permission_denials": tc.in})

			got := decodeStoredDenials(t, dst["permission_denials"])
			named := map[string]bool{}
			var sentinel *storedDenial
			for i, d := range got {
				if d.Type == unrecognisedShape {
					sentinel = &got[i]
					continue
				}
				named[d.ToolName] = true
			}
			for _, want := range tc.wantTools {
				if !named[want] {
					t.Errorf("permission_denials = %+v, lost the readable denial of %q", got, want)
				}
			}
			if sentinel == nil {
				t.Fatalf("permission_denials = %+v: %d entries were unreadable and the row says so nowhere — "+
					"an operator reads the tools it DID name as the whole list", got, tc.wantUnreadable)
			}
			if sentinel.Count != tc.wantUnreadable {
				t.Errorf("sentinel count = %d, want %d — how many refusals went unnamed is the only way "+
					"to tell a nearly-complete list from a nearly-empty one", sentinel.Count, tc.wantUnreadable)
			}
			if sentinel.ToolName != "" {
				t.Errorf("sentinel names a tool (%q) we never read", sentinel.ToolName)
			}
		})
	}
}

// dropEmptyObjects shrinks the stored list; the journal entry carries
// mcp_server_error_count alongside it so a reader can tell three skipped
// servers from the one that happened to project. The run record stored the list
// alone, so `run get` and `run list` reported fewer skipped servers than the CLI
// did, with no field to contradict them.
func TestMergeSessionInitMeta_KeepsTheSkipCountWhenEntriesDrop(t *testing.T) {
	cases := []struct {
		name      string
		in        any
		wantCount int
		wantKept  int
	}{
		{
			name:      "one readable, two renamed",
			in:        json.RawMessage(`[{"name":"memory","type":"invalid_config"},{"server":"sentry"},{"server":"linear"}]`),
			wantCount: 3,
			wantKept:  1,
		},
		{
			name:      "all readable",
			in:        json.RawMessage(`[{"name":"memory","type":"invalid_config"},{"name":"sentry","type":"url_missing_type"}]`),
			wantCount: 2,
			wantKept:  2,
		},
		{
			// Nothing readable: the sentinel already stood in for the list, but
			// the count says how much it stands for.
			name:      "all renamed",
			in:        json.RawMessage(`[{"server":"memory"},{"server":"sentry"}]`),
			wantCount: 2,
			wantKept:  1, // the sentinel
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{"mcp_server_errors": tc.in})

			errs, _ := dst["mcp_server_errors"].([]map[string]any)
			if len(errs) != tc.wantKept {
				t.Fatalf("mcp_server_errors = %+v, want %d stored entries", dst["mcp_server_errors"], tc.wantKept)
			}
			if got := dst["mcp_server_error_count"]; got != tc.wantCount {
				t.Errorf("mcp_server_error_count = %v, want %d — the run record otherwise reports %d skipped "+
					"servers where the CLI reported %d", got, tc.wantCount, len(errs), tc.wantCount)
			}
		})
	}
}

// boundInitObjects returns whether it cut the list and the run record threw the
// flag away, so a run with more than sessionInitListMax skipped servers recorded
// the cap with no marker — while the journal entry for the SAME run wrote
// mcp_server_errors_truncated. Two durable records that disagree about whether
// the list is complete, with no way to tell which one to believe.
func TestMergeSessionInitMeta_MarksTheSkipListTruncated(t *testing.T) {
	entries := make([]string, 0, sessionInitListMax+8)
	for i := 0; i < sessionInitListMax+8; i++ {
		entries = append(entries, fmt.Sprintf(`{"name":"server%02d","type":"invalid_config"}`, i))
	}

	cases := []struct {
		name          string
		in            json.RawMessage
		wantTruncated bool
		wantCount     int
	}{
		{
			name:          "more skipped servers than the cap keeps",
			in:            json.RawMessage("[" + strings.Join(entries, ",") + "]"),
			wantTruncated: true,
			wantCount:     sessionInitListMax + 8,
		},
		{
			name:          "a list that fits is not marked",
			in:            json.RawMessage("[" + strings.Join(entries[:2], ",") + "]"),
			wantTruncated: false,
			wantCount:     2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{"mcp_server_errors": tc.in})

			if got := dst["mcp_server_error_count"]; got != tc.wantCount {
				t.Errorf("mcp_server_error_count = %v, want %d", got, tc.wantCount)
			}
			_, marked := dst["mcp_server_errors_truncated"]
			if marked != tc.wantTruncated {
				t.Errorf("mcp_server_errors_truncated present = %v, want %v — permission_denials got this "+
					"marker in the same commit, and the session_init entry writes it for this same run",
					marked, tc.wantTruncated)
			}
		})
	}
}

// emptyJSONValue is the gate in front of both projections, and it only
// recognised emptiness in the shapes the CLAUDE adapter happens to produce —
// raw JSON. An already-decoded empty list ([]map[string]any{}, []string{},
// []json.RawMessage{}) fell through as "content", so a session where NOTHING
// was skipped and NOTHING was denied got a degraded alarm and a sentinel that
// names a problem that does not exist. Any adapter that decodes before stamping
// the event lands here.
func TestEmptyDecodedListsAreNotAReport(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"decoded objects", []map[string]any{}},
		{"raw JSON elements", []json.RawMessage{}},
		{"strings", []string{}},
		{"decoded any", []any{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := map[string]any{}
			MergeSessionInitMeta(dst, map[string]any{"mcp_server_errors": tc.in})
			if v, ok := dst["mcp_server_errors"]; ok {
				t.Errorf("mcp_server_errors = %v for an EMPTY report — the run record now claims a "+
					"server was skipped when none was", v)
			}
			MergeResultUsageMeta(dst, map[string]any{"permission_denials": tc.in})
			if v, ok := dst["permission_denials"]; ok {
				t.Errorf("permission_denials = %v for an EMPTY report — the run record now claims a "+
					"tool was refused when none was", v)
			}
		})
	}
}
