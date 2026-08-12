package orchestrator

import (
	"encoding/json"
	"strings"
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

// Same argument as the skills test above, applied to the four fields added
// alongside it — and the result line is the costlier one to lose. When the
// unmarshal fails the parser's fallback dumps the envelope into the chat as
// text, so the run emits no result event at all: no cost, no usage, and
// inBandFailure.observe never sees the is_error it keys on, which is how a run
// that failed gets recorded COMPLETED. api_error_status is the likeliest to
// bite — a release that emits "401" as a string would take every result line
// with it.
func TestParseClaudeStream_ResultSurvivesAnUnexpectedFieldShape(t *testing.T) {
	cases := []struct {
		name  string
		field string
		why   string
	}{
		{"api_error_status as a string", `"api_error_status":"401"`, "the shape most likely to change, and the one that names the difference between a bad credential and a busy API"},
		{"api_error_status as an object", `"api_error_status":{"code":401}`, "a status that grew a body must not cost the envelope"},
		{"terminal_reason as an object", `"terminal_reason":{"kind":"api_error"}`, "a reason that grew structure is still only a label"},
		{"stop_reason as an array", `"stop_reason":["stop_sequence"]`, "same"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"type":"result","subtype":"success","is_error":true,` + tc.field +
				`,"num_turns":2,"total_cost_usd":0.42,"usage":{"input_tokens":11}}`

			events := collectEvents(t, line)
			for _, e := range events {
				if e.Type == "text" {
					t.Fatalf("result line fell back to raw text (%.60q…) — one unexpected field took the whole envelope with it: %s", e.Content, tc.why)
				}
			}
			meta := metaOf(t, events, "result")
			// The rest of the envelope is what finalization actually spends:
			// cost and usage are recorded from here, and is_error is the only
			// in-band signal that the run failed.
			if got := meta["total_cost_usd"]; got != 0.42 {
				t.Errorf("total_cost_usd = %v, want 0.42 — the run would be finalised with no cost", got)
			}
			if got := meta["is_error"]; got != true {
				t.Errorf("is_error = %v, want true — a failed run would be recorded COMPLETED", got)
			}
			if _, ok := meta["usage"]; !ok {
				t.Error("usage dropped — the run would be finalised with no token accounting")
			}
		})
	}
}

// A quoted number is the one shape change worth absorbing rather than
// discarding: "401" means 401, and the status is the field inBandFailure.label
// falls back to when nothing else names the cause ("HTTP 401" vs a bare
// "failed run"). It must arrive as an int, not a tolerant wrapper — metaInt
// type-switches on int and meta["api_error_status"] crosses into the journal.
func TestParseClaudeStream_ResultCoercesAQuotedAPIErrorStatus(t *testing.T) {
	meta := metaOf(t, collectEvents(t,
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":"401"}`), "result")

	if got := meta["api_error_status"]; got != 401 {
		t.Errorf("api_error_status = %v (%T), want int 401 — a quoted status still names the credential failure", got, got)
	}
}

// The tolerant-type round stopped one field short of the failure it describes.
// terminal_reason, stop_reason, api_error_status and capabilities were made
// tolerant; is_error, total_cost_usd and num_turns were left strict — and
// is_error is the single most consequential field on this envelope. Losing the
// result line costs the cost and the usage, but losing is_error costs the
// VERDICT: inBandFailure.observe keys on meta["is_error"].(bool), so a run that
// failed to authenticate is finalised COMPLETED and nobody is told.
//
// Two things have to hold for each row, and they are different things. The
// envelope must survive (no raw-text fallback, cost and usage still there) —
// that is blast radius. And the field's MEANING must survive: "true" is true,
// "0.42" is 0.42. A mechanism that only isolates the bad field would zero
// is_error, which is the same silent COMPLETED by a shorter route.
//
// Every value below is asserted against a plain Go type, not a wrapper: this
// metadata crosses into the journal and is type-asserted there.
func TestParseClaudeStream_ResultSurvivesAQuotedScalar(t *testing.T) {
	cases := []struct {
		name   string
		fields string
		key    string
		want   interface{}
		why    string
	}{
		{
			"is_error as a quoted bool", `"is_error":"true","total_cost_usd":0.42`,
			"is_error", true,
			"the only in-band signal that the run failed — dropped, the run is finalised COMPLETED",
		},
		{
			"is_error as 1", `"is_error":1,"total_cost_usd":0.42`,
			"is_error", true,
			"a truthy number still means the run failed",
		},
		{
			"total_cost_usd as a quoted number", `"is_error":true,"total_cost_usd":"0.42"`,
			"total_cost_usd", 0.42,
			"cost is recorded from this field and nowhere else",
		},
		{
			"num_turns as a quoted number", `"is_error":true,"total_cost_usd":0.42,"num_turns":"3"`,
			"num_turns", 3,
			"inBandFailure reports the turn count back to the operator",
		},
		{
			"a field nobody made tolerant", `"is_error":true,"total_cost_usd":0.42,"duration_ms":"12"`,
			"is_error", true,
			"the next shape change will be on a field this list does not name — it must still cost only that field",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"type":"result","subtype":"success",` + tc.fields + `,"usage":{"input_tokens":11}}`

			events := collectEvents(t, line)
			for _, e := range events {
				if e.Type == "text" {
					t.Fatalf("result line fell back to raw text (%.60q…) — one unexpected field took the whole envelope with it: %s", e.Content, tc.why)
				}
			}
			meta := metaOf(t, events, "result")
			if got := meta[tc.key]; got != tc.want {
				t.Errorf("meta[%q] = %v (%T), want %v (%T) — %s", tc.key, got, got, tc.want, tc.want, tc.why)
			}
			// Whatever the row was about, the verdict and the money must be
			// intact: these are what finalization spends.
			if got, ok := meta["is_error"].(bool); !ok || !got {
				t.Errorf("is_error = %v (%T), want bool true — inBandFailure.observe type-asserts bool, so anything else records a failed run COMPLETED", meta["is_error"], meta["is_error"])
			}
			if got := meta["total_cost_usd"]; got != 0.42 {
				t.Errorf("total_cost_usd = %v (%T), want float64 0.42 — the run would be finalised with no cost", got, got)
			}
			if _, ok := meta["usage"]; !ok {
				t.Error("usage dropped — the run would be finalised with no token accounting")
			}
		})
	}
}

// The nested `message` object is decoded in a second pass, so it needs the same
// rule or the fix has a hole exactly where the tool calls live: one block that
// grew a field of an unexpected shape must not take the blocks either side of
// it. An assistant line carries every tool_use of a turn, so insisting the
// nested decode be error-free costs the whole turn's tool activity — the UI
// shows an agent that thought and did nothing.
func TestParseClaudeStream_AssistantKeepsTheBlocksAroundABadOne(t *testing.T) {
	const line = `{"type":"assistant","message":{"content":[
		{"type":"tool_use","id":123,"name":"Bash","input":{"command":"ls"}},
		{"type":"tool_use","id":"tu-2","name":"Grep","input":{"pattern":"x"}}]}}`

	var names []string
	for _, e := range collectEvents(t, line) {
		if e.Type == "text" {
			t.Fatalf("assistant line fell back to raw text (%.60q…)", e.Content)
		}
		if e.Type == "tool_call" {
			names = append(names, e.Content)
		}
	}
	if len(names) != 2 || names[0] != "Bash" || names[1] != "Grep" {
		t.Errorf("tool_call events = %v, want [Bash Grep] — a numeric id on one block cost the whole turn's tool activity", names)
	}
}

// The other half of the contract: a line that genuinely carries nothing must
// still reach the transcript as text. Tolerating a bad FIELD must not turn into
// swallowing a bad LINE — a CLI that starts writing plain-text diagnostics to
// stdout would otherwise go silent instead of visibly wrong.
func TestParseClaudeStream_FallsBackToTextWhenNothingSurvives(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"not JSON at all", "not json line"},
		{"a truncated object", `{"type":"result","is_error":`},
		{"a JSON array", `["result"]`},
		{"a bare JSON string", `"result"`},
		{"an object whose type is not a string", `{"type":["result"],"is_error":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := collectEvents(t, tc.line)
			if len(events) != 1 || events[0].Type != "text" {
				t.Fatalf("got %+v, want a single text event — a line with no routable type must stay visible", events)
			}
		})
	}
}

// The init line's capabilities is documented upstream as an array of strings —
// which is exactly the kind of assurance that let a pinned CLI version drift
// for a hundred releases (#1932). It is a pass-through field with no typed
// consumer, so nothing is lost by refusing to bet the whole init line on the
// documentation staying true.
func TestParseClaudeStream_InitSurvivesAnUnexpectedCapabilitiesShape(t *testing.T) {
	const line = `{"type":"system","subtype":"init","model":"claude-opus-5",
		"claude_code_version":"2.1.300",
		"capabilities":{"interrupt_receipt_v1":true}}`

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

// …and the documented shape must still arrive as a plain []string: the value
// goes into event metadata that crosses into the journal, and a wrapper type
// there would break any consumer that type-asserts it.
func TestParseClaudeStream_InitKeepsCapabilitiesTyped(t *testing.T) {
	meta := metaOf(t, collectEvents(t,
		`{"type":"system","subtype":"init","capabilities":["interrupt_receipt_v1","msg_lifecycle_v1"]}`), "system")

	got, ok := meta["capabilities"].([]string)
	if !ok {
		t.Fatalf("capabilities = %T, want []string", meta["capabilities"])
	}
	if len(got) != 2 || got[0] != "interrupt_receipt_v1" {
		t.Errorf("capabilities = %v, want the two entries the line carried", got)
	}
}

// subtype is the SECOND discriminator, and the tolerant-type round left it
// strict — which made it the one field on this envelope whose loss is not the
// loss of one field. Observed against the shipped parser:
//
//	{"type":"system","subtype":["init"],"model":"claude-opus-5", …}
//	  -> the emitted event's whole metadata was  map[subtype:]
//
// msg.Type decoded, so the line-level isolation (err != nil && msg.Type == "")
// correctly kept the line — and msg.Subtype zeroed, so `switch msg.Subtype`
// matched nothing and the init branch that copies every provenance field never
// ran. No version, no session id, no mcp_server_errors, no raw line either:
// strictly worse than the pre-tolerance behaviour, which at least dumped the
// envelope into the transcript. The same field on `result` carries the
// error_max_turns classification and the label inBandFailure.Err() shows a user.
func TestParseClaudeStream_SubtypeSurvivesAnUnexpectedShape(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		eventType string
		subtype   string
		key       string
		want      interface{}
		why       string
	}{
		{
			name: "init subtype wrapped in an array",
			line: `{"type":"system","subtype":["init"],"model":"claude-opus-5",
				"claude_code_version":"2.1.226","session_id":"s-1"}`,
			eventType: "system", subtype: "init",
			key: "claude_code_version", want: "2.1.226",
			why: "the init branch is the only thing that copies session provenance — a subtype that routes nowhere drops all of it",
		},
		{
			name:      "result subtype wrapped in an array",
			line:      `{"type":"result","subtype":["error_max_turns"],"is_error":true,"num_turns":50}`,
			eventType: "result", subtype: "error_max_turns",
			key: "is_error", want: true,
			why: "error_max_turns is the classification that turns a failed run into a message naming a limit the operator can raise",
		},
		{
			name:      "subtype as a bare number still names a branch",
			line:      `{"type":"system","subtype":0,"model":"claude-opus-5"}`,
			eventType: "system", subtype: "0",
			key: "subtype", want: "0",
			why: "a scalar still reads as a label; keeping it verbatim beats erasing the discriminator",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := collectEvents(t, tc.line)
			for _, e := range events {
				if e.Type == "text" {
					t.Fatalf("line fell back to raw text (%.60q…) — the envelope must survive", e.Content)
				}
			}
			meta := metaOf(t, events, tc.eventType)
			// A plain string, not a wrapper: inBandFailure.observe and the
			// buffering handler's init gate both type-assert meta["subtype"].
			got, ok := meta["subtype"].(string)
			if !ok {
				t.Fatalf("meta[\"subtype\"] = %T, want string — every consumer type-asserts it", meta["subtype"])
			}
			if got != tc.subtype {
				t.Errorf("meta[\"subtype\"] = %q, want %q — %s", got, tc.subtype, tc.why)
			}
			if v := meta[tc.key]; v != tc.want {
				t.Errorf("meta[%q] = %v (%T), want %v — %s", tc.key, v, v, tc.want, tc.why)
			}
		})
	}
}

// The end of the same story: the recovered subtype has to reach the code that
// acts on it, not just the metadata map. error_max_turns is the in-band failure
// operators meet most often (the cap is stamped on every run), and its message
// is the only one that names a limit they can raise.
func TestInBandFailure_ClassifiesAWrappedMaxTurnsSubtype(t *testing.T) {
	var f inBandFailure
	parseClaudeCodeStreamJSON(
		[]byte(`{"type":"result","subtype":["error_max_turns"],"is_error":true,"num_turns":50}`),
		f.observe)

	err := f.Err()
	if err == nil {
		t.Fatal("no in-band failure recorded from a result envelope with is_error true")
	}
	if !strings.Contains(err.Error(), "turn cap") {
		t.Errorf("Err() = %q, want the turn-cap message — an unreadable subtype demotes it to the generic "+
			"\"check the journal\" text, which tells the operator nothing about a limit they can raise", err)
	}
}

// A subtype we genuinely cannot reduce to a label must not be silent either.
// This is the same bar finding 2 sets for strict fields, applied to the
// tolerance itself: a tolerant type that quietly returns "" would put the bug
// above back in a new place, because "" is also what a CLI that sent no subtype
// produces.
func TestParseClaudeStream_UnreadableSubtypeIsReported(t *testing.T) {
	meta := metaOf(t, collectEvents(t,
		`{"type":"system","subtype":{"name":"init"},"model":"claude-opus-5"}`), "system")

	if got, _ := meta["subtype"].(string); got != "" {
		t.Errorf("meta[\"subtype\"] = %q, want empty — an object names no branch and must not be guessed at", got)
	}
	note, _ := meta["decode_error"].(string)
	if note == "" {
		t.Fatal("no decode_error on an event whose discriminator could not be read — indistinguishable from a CLI that sent no subtype")
	}
	if !strings.Contains(note, "subtype") {
		t.Errorf("decode_error = %q, want it to name subtype", note)
	}
}

// Finding 2. The line-level isolation was narrowed to `err != nil && msg.Type
// == ""` — right, because a line that routed is not a line that failed — but it
// means a field json SKIPPED is now gone with nothing said about it. `result`
// is the sharp case: it is the CLI's final user-facing message, inBandFailure
// quotes it, and a release that emits it as content blocks (the shape Anthropic
// uses for message content everywhere else) would show an operator "agent
// reported a failed run (api_error 401):" and nothing at all — identical, from
// the outside, to a CLI that sent no message.
//
// The bar: a field we could not read must not be indistinguishable from a field
// the CLI did not send.
func TestParseClaudeStream_PartialDecodeIsNotSilent(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		eventType string
		field     string
		why       string
	}{
		{
			name: "result as content blocks",
			line: `{"type":"result","subtype":"success","is_error":true,
				"result":[{"type":"text","text":"Not logged in · Please run /login"}]}`,
			eventType: "result", field: "result",
			why: "the CLI's final message, and the detail inBandFailure quotes back to the user",
		},
		{
			name:      "model as an object on init",
			line:      `{"type":"system","subtype":"init","model":{"id":"claude-opus-5"},"claude_code_version":"2.1.226"}`,
			eventType: "system", field: "model",
			why: "resolved-vs-requested model is recorded from this field and nowhere else",
		},
		{
			name:      "session_id as a number",
			line:      `{"type":"result","subtype":"success","session_id":1234,"is_error":false}`,
			eventType: "result", field: "session_id",
			why: "the CLI's own correlation key for the run",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := collectEvents(t, tc.line)
			for _, e := range events {
				if e.Type == "text" {
					t.Fatalf("line fell back to raw text (%.60q…) — the isolation must stay", e.Content)
				}
			}
			meta := metaOf(t, events, tc.eventType)
			note, _ := meta["decode_error"].(string)
			if note == "" {
				t.Fatalf("no decode_error on a line that lost %s — %s", tc.field, tc.why)
			}
			if !strings.Contains(note, tc.field) {
				t.Errorf("decode_error = %q, want it to name the field it lost (%s)", note, tc.field)
			}
		})
	}
}

// …and the marker must mean something, which it only does if a healthy line
// never carries it. Every event a clean line produces has to come out exactly as
// it did before, or "this run lost a field" becomes noise nobody reads.
func TestParseClaudeStream_CleanLinesCarryNoDecodeMarker(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","claude_code_version":"2.1.226","capabilities":["interrupt_receipt_v1"]}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.42,"result":"done","usage":{"input_tokens":11}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}`,
	}
	for _, line := range lines {
		for _, e := range collectEvents(t, line) {
			meta, _ := e.Metadata.(map[string]interface{})
			if _, ok := meta["decode_error"]; ok {
				t.Errorf("clean line %.40q… produced a %s event carrying decode_error", line, e.Type)
			}
		}
	}
}
