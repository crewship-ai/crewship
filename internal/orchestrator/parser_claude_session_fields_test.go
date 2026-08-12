package orchestrator

import (
	"encoding/json"
	"testing"
)

// The init and result envelopes carry more than the parser used to keep, and
// the fields it dropped are exactly the ones that would have caught the --bare
// tool clamp (#1932): the adapter was validated against 2.1.126 while agent
// containers install the `claude-code:2` devcontainer feature, i.e. latest, and
// nothing in a run recorded which version actually answered. Both payloads
// below are trimmed copies of real lines from 2.1.226.

// collectEvents drives the Claude parser over one stream line and returns every
// event it emitted, so a test can assert on metadata without a live run.
func collectEvents(t *testing.T, line string) []AgentEvent {
	t.Helper()
	var got []AgentEvent
	parseClaudeCodeStreamJSON([]byte(line), func(e AgentEvent) { got = append(got, e) })
	return got
}

// metaOf returns the metadata map of the first event of the given type.
func metaOf(t *testing.T, events []AgentEvent, typ string) map[string]interface{} {
	t.Helper()
	for _, e := range events {
		if e.Type != typ {
			continue
		}
		meta, ok := e.Metadata.(map[string]interface{})
		if !ok {
			t.Fatalf("%s event carries %T metadata, want map", typ, e.Metadata)
		}
		return meta
	}
	t.Fatalf("no %s event emitted from %d events", typ, len(events))
	return nil
}

func TestParseClaudeStream_InitCarriesSessionProvenance(t *testing.T) {
	const line = `{"type":"system","subtype":"init","cwd":"/output/ada",
		"session_id":"e0e80a31-cceb-4df9-929d-6a07e7984399",
		"claude_code_version":"2.1.226","apiKeySource":"ANTHROPIC_API_KEY",
		"permissionMode":"bypassPermissions","model":"claude-opus-5",
		"tools":["Read","Write"],"skills":["code-review"],
		"capabilities":["interrupt_receipt_v1","msg_lifecycle_v1"],
		"mcp_servers":[{"name":"crewship-memory","status":"connected"}],
		"mcp_server_errors":[{"name":"composio","type":"url_missing_type","message":"url entry has no type"}]}`

	meta := metaOf(t, collectEvents(t, line), "system")

	cases := []struct {
		key  string
		want interface{}
		why  string
	}{
		{"claude_code_version", "2.1.226", "the version that actually answered — without it, adapter drift is invisible until a capability quietly goes missing"},
		{"session_id", "e0e80a31-cceb-4df9-929d-6a07e7984399", "the CLI's own correlation key for the run"},
		{"apiKeySource", "ANTHROPIC_API_KEY", "which auth path resolved, i.e. whether the credential we mounted is the one in use"},
		{"permissionMode", "bypassPermissions", "proof --dangerously-skip-permissions took effect"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := meta[tc.key]; got != tc.want {
				t.Errorf("meta[%q] = %v, want %v — %s", tc.key, got, tc.want, tc.why)
			}
		})
	}

	// capabilities is how a run says what protocol behaviours it supports.
	// Feature-detecting on it beats comparing version strings, which is the
	// habit that let 2.1.126 stay pinned for a hundred releases.
	if _, ok := meta["capabilities"]; !ok {
		t.Error("capabilities dropped — nothing else in the stream reports what this CLI can do")
	}

	// A skipped MCP server is not an error the run reports any other way: the
	// entry is dropped at validation, the run continues and exits 0. An agent
	// that lost crewship-memory this way looks healthy.
	raw, ok := meta["mcp_server_errors"].(json.RawMessage)
	if !ok {
		t.Fatalf("mcp_server_errors = %T, want json.RawMessage — a silently skipped MCP server is a silent capability loss", meta["mcp_server_errors"])
	}
	var errs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &errs); err != nil || len(errs) != 1 || errs[0].Name != "composio" {
		t.Errorf("mcp_server_errors = %s (err %v), want the composio entry", raw, err)
	}

	// skills tells us whether the SKILL.md files we materialise were actually
	// discovered — today they are not (project skill discovery is off under
	// --setting-sources ""), and this is the field that shows it.
	if _, ok := meta["skills"]; !ok {
		t.Error("skills dropped — the only per-run evidence that materialised skills were discovered")
	}
}

// The init line is one JSON object, so a field whose shape we guessed wrong
// does not fail alone: it fails the whole unmarshal, and the parser's fallback
// dumps the entire line into the transcript as raw text. `skills` is not in the
// published stream reference — 2.1.204 and 2.1.226 both send an array of
// strings, but nothing promises that — so it is carried untyped. This pins the
// blast radius rather than the shape.
func TestParseClaudeStream_InitSurvivesAnUnexpectedSkillsShape(t *testing.T) {
	const line = `{"type":"system","subtype":"init","model":"claude-opus-5",
		"claude_code_version":"2.1.300",
		"skills":[{"name":"code-review","source":"plugin"}]}`

	events := collectEvents(t, line)
	for _, e := range events {
		if e.Type == "text" {
			t.Fatalf("init line fell back to raw text (%.60q…) — one unexpected field took the whole event with it", e.Content)
		}
	}
	meta := metaOf(t, events, "system")
	if got := meta["claude_code_version"]; got != "2.1.300" {
		t.Errorf("claude_code_version = %v, want 2.1.300 — the rest of the line must still parse", got)
	}
}

// An init line without the newer fields must not gain empty keys: an absent
// mcp_server_errors means "no server was skipped", and a key present-but-empty
// would make a CI gate that fails on a non-empty array read the wrong thing.
func TestParseClaudeStream_InitOmitsAbsentFields(t *testing.T) {
	meta := metaOf(t, collectEvents(t,
		`{"type":"system","subtype":"init","model":"claude-opus-5","cwd":"/output/ada"}`), "system")

	for _, key := range []string{"claude_code_version", "session_id", "apiKeySource", "permissionMode", "capabilities", "skills", "mcp_server_errors"} {
		if v, ok := meta[key]; ok {
			t.Errorf("meta[%q] = %v on a line that never carried it; absent must stay absent", key, v)
		}
	}
}

func TestParseClaudeStream_ResultCarriesTerminalReason(t *testing.T) {
	const line = `{"type":"result","subtype":"success","is_error":true,
		"terminal_reason":"api_error","api_error_status":401,
		"stop_reason":"stop_sequence","session_id":"e0e80a31",
		"result":"Not logged in · Please run /login","num_turns":1,
		"permission_denials":[{"tool_name":"Bash"}],"total_cost_usd":0}`

	meta := metaOf(t, collectEvents(t, line), "result")

	// The auth failure above is the case that motivated this: subtype says
	// "success" while is_error is true, so subtype alone cannot name a cause.
	if got := meta["terminal_reason"]; got != "api_error" {
		t.Errorf("terminal_reason = %v, want api_error — it is the only field on this envelope that names the cause", got)
	}
	if got := meta["api_error_status"]; got != 401 {
		t.Errorf("api_error_status = %v (%T), want 401 — 401 vs 529 is the difference between a bad credential and a busy API", got, got)
	}
	if got := meta["stop_reason"]; got != "stop_sequence" {
		t.Errorf("stop_reason = %v, want stop_sequence", got)
	}
	if got := meta["session_id"]; got != "e0e80a31" {
		t.Errorf("session_id = %v, want e0e80a31", got)
	}
	if _, ok := meta["permission_denials"]; !ok {
		t.Error("permission_denials dropped — a run blocked by permissions otherwise looks like a run that chose not to act")
	}
}
