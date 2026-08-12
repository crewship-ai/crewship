package orchestrator

// #1934: the CLI's session-init event is the only place a SKIPPED --mcp-config
// entry is ever reported. The run then continues and exits 0, so an agent that
// lost crewship-memory to a bad config looks perfectly healthy while being
// quietly less capable. These tests pin the entry that breaks that silence —
// and, just as importantly, pin what must NEVER reach its payload.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func claudeInitMeta() map[string]any {
	return map[string]any{
		"subtype":             "init",
		"model":               "claude-opus-4-8",
		"tools":               []string{"Bash", "Read", "Edit"},
		"cwd":                 "/workspace",
		"mcp_servers":         []json.RawMessage{json.RawMessage(`{"name":"crewship-memory","status":"connected"}`)},
		"claude_code_version": "2.1.219",
		"session_id":          "sess-abc",
		"apiKeySource":        "ANTHROPIC_API_KEY",
		"permissionMode":      "bypassPermissions",
		"capabilities":        []string{"interrupt_receipt_v1", "tool_result_v2"},
		"skills":              json.RawMessage(`[{"name":"code-review"},{"name":"triage"}]`),
	}
}

func sessionInitReq() AgentRunRequest {
	return AgentRunRequest{
		WorkspaceID: "ws1",
		CrewID:      "crew1",
		AgentID:     "a1",
		MissionID:   "m1",
		AgentSlug:   "researcher",
		ChatID:      "chat1",
		CLIAdapter:  "CLAUDE_CODE",
	}
}

func TestEmitSessionInitSignal_RecordsBoundedProvenance(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	o.emitSessionInitSignal(context.Background(), sessionInitReq(), claudeInitMeta())

	if len(cj.entries) != 1 {
		t.Fatalf("expected exactly one journal entry, got %d", len(cj.entries))
	}
	e := cj.entries[0]
	if e.Type != "run.session_init" {
		t.Errorf("Type = %q, want run.session_init", e.Type)
	}
	if e.Severity != "info" {
		t.Errorf("Severity = %q, want info — nothing was skipped", e.Severity)
	}
	if e.WorkspaceID != "ws1" || e.CrewID != "crew1" || e.AgentID != "a1" || e.MissionID != "m1" {
		t.Errorf("scope not propagated onto entry: %+v", e)
	}
	if e.ActorType != "agent" || e.ActorID != "a1" {
		t.Errorf("actor = %s/%s, want agent/a1", e.ActorType, e.ActorID)
	}
	if e.Summary == "" {
		t.Error("Summary is required by journal.Entry.Validate and is what a human reads")
	}
	for k, want := range map[string]any{
		"model":           "claude-opus-4-8",
		"cli_version":     "2.1.219",
		"session_id":      "sess-abc",
		"api_key_source":  "ANTHROPIC_API_KEY",
		"permission_mode": "bypassPermissions",
		"cwd":             "/workspace",
		"agent_slug":      "researcher",
		"cli_adapter":     "CLAUDE_CODE",
	} {
		if got := e.Payload[k]; got != want {
			t.Errorf("payload[%q] = %v, want %v", k, got, want)
		}
	}
	for k, want := range map[string]int{
		"tool_count":       3,
		"capability_count": 2,
		"skill_count":      2,
		"mcp_server_count": 1,
	} {
		if got := e.Payload[k]; got != want {
			t.Errorf("payload[%q] = %v, want %d", k, got, want)
		}
	}
	servers, _ := e.Payload["mcp_servers"].([]map[string]any)
	if len(servers) != 1 || servers[0]["name"] != "crewship-memory" || servers[0]["status"] != "connected" {
		t.Errorf("mcp_servers payload = %+v, want one {name,status} pair", e.Payload["mcp_servers"])
	}
	// Nothing was skipped, so the key must stay ABSENT — a consumer gating on
	// it can only trust absence if absence keeps meaning "nothing was skipped".
	if _, ok := e.Payload["mcp_server_errors"]; ok {
		t.Errorf("mcp_server_errors must be absent when no server was skipped: %+v", e.Payload)
	}
}

// The escalation is the whole point: a skipped MCP server is a silent
// capability loss on a run that exits 0, so it must be loud in the feed.
func TestEmitSessionInitSignal_EscalatesWhenAServerWasSkipped(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	meta := claudeInitMeta()
	meta["mcp_server_errors"] = json.RawMessage(
		`[{"name":"crewship-memory","type":"invalid_config","message":"boom"},` +
			`{"name":"composio","type":"url_missing_type","message":"url entry has no type"}]`)

	o.emitSessionInitSignal(context.Background(), sessionInitReq(), meta)

	if len(cj.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cj.entries))
	}
	e := cj.entries[0]
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want error — the agent is running without a server it was configured with", e.Severity)
	}
	if e.Payload["mcp_server_error_count"] != 2 {
		t.Errorf("mcp_server_error_count = %v, want 2", e.Payload["mcp_server_error_count"])
	}
	errs, _ := e.Payload["mcp_server_errors"].([]map[string]any)
	if len(errs) != 2 {
		t.Fatalf("mcp_server_errors payload = %+v, want 2 entries", e.Payload["mcp_server_errors"])
	}
	if errs[0]["name"] != "crewship-memory" || errs[0]["type"] != "invalid_config" {
		t.Errorf("first skipped server = %+v, want name+type carried", errs[0])
	}
	// A human scanning the feed must be able to tell WHICH server vanished
	// without opening the payload.
	if !strings.Contains(e.Summary, "crewship-memory") {
		t.Errorf("summary must name the skipped server, got %q", e.Summary)
	}
	if !strings.Contains(strings.ToLower(e.Summary), "skip") {
		t.Errorf("summary must say the server was skipped, got %q", e.Summary)
	}
}

// THE test. Journal payloads bypass the credential scrubber (the tap that
// feeds this sits BEFORE it in stream → scrub → journalTap → user) and journal
// rows are hash-chained and append-only, so a secret written here is written
// forever. mcp_server_errors[].message is free text out of a config file — the
// single highest-risk field on the init event. It must not reach the entry in
// any form, and the closed-category `type` is what carries the meaning instead.
func TestEmitSessionInitSignal_NeverCarriesTheFreeTextMessage(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	const secret = "sk-ant-api03-NOTAREALKEYbutTREATITASONE"
	meta := claudeInitMeta()
	meta["mcp_server_errors"] = json.RawMessage(
		`[{"name":"crewship-memory","type":"invalid_config","message":"failed to reach https://mcp.internal/?token=` + secret + `"}]`)

	o.emitSessionInitSignal(context.Background(), sessionInitReq(), meta)

	if len(cj.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cj.entries))
	}
	// Serialise the ENTIRE entry — summary, payload, refs. A leak anywhere in
	// the row is a leak, and asserting on one field would miss the next one.
	blob, err := json.Marshal(cj.entries[0])
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("free-text message reached the journal entry — it is unscrubbed and unredactable: %s", blob)
	}
	if strings.Contains(string(blob), "failed to reach") {
		t.Fatalf("free-text message reached the journal entry: %s", blob)
	}
	if strings.Contains(string(blob), "message") {
		t.Fatalf("the message field must not be carried under any key: %s", blob)
	}
	// …while the closed-category fields that make the entry actionable ARE
	// carried.
	if !strings.Contains(string(blob), "invalid_config") || !strings.Contains(string(blob), "crewship-memory") {
		t.Errorf("name + type must survive: %s", blob)
	}
}

// Gemini/codex/opencode report a bare {subtype, model, session_id}. The entry
// must still be sane: no invented zero counts, a usable summary, severity info.
func TestEmitSessionInitSignal_MinimalAdapterMeta(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	o.emitSessionInitSignal(context.Background(), sessionInitReq(), map[string]any{
		"subtype": "init",
		"model":   "gemini-2.5-pro",
	})

	if len(cj.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cj.entries))
	}
	e := cj.entries[0]
	if e.Severity != "info" {
		t.Errorf("Severity = %q, want info", e.Severity)
	}
	if e.Payload["model"] != "gemini-2.5-pro" {
		t.Errorf("model = %v, want gemini-2.5-pro", e.Payload["model"])
	}
	if !strings.Contains(e.Summary, "gemini-2.5-pro") {
		t.Errorf("summary should name the model it did report, got %q", e.Summary)
	}
	// "0 tools" would be a lie: the adapter never reported an inventory.
	for _, k := range []string{"tool_count", "capability_count", "skill_count", "mcp_server_count", "cwd", "session_id"} {
		if _, ok := e.Payload[k]; ok {
			t.Errorf("payload[%q] present for an adapter that never reported it: %+v", k, e.Payload)
		}
	}
	if strings.Contains(e.Summary, "0 ") {
		t.Errorf("summary invents an inventory the adapter never reported: %q", e.Summary)
	}
}

// apiKeySource is upstream free text that lands in a permanent row, so it goes
// through the same closed-set sanitiser the log line uses — never verbatim.
func TestEmitSessionInitSignal_APIKeySourceIsConstrained(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	meta := claudeInitMeta()
	meta["apiKeySource"] = "/home/agent/.creds/anthropic-token"
	o.emitSessionInitSignal(context.Background(), sessionInitReq(), meta)

	if got := cj.entries[0].Payload["api_key_source"]; got != "other" {
		t.Errorf("api_key_source = %v, want \"other\" — an unknown source must not be quoted", got)
	}
}

// Nothing downstream caps a journal payload's size, so the caps live at the
// emit site: a CLI reporting 500 tools must not be able to write a 500-element
// row, and the count must stay truthful when the list is cut.
func TestEmitSessionInitSignal_BoundsListShapedFields(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	var servers []json.RawMessage
	var errs []json.RawMessage
	for i := 0; i < sessionInitListMax+7; i++ {
		servers = append(servers, json.RawMessage(`{"name":"srv","status":"connected"}`))
		errs = append(errs, json.RawMessage(`{"name":"srv","type":"invalid_config"}`))
	}
	meta := claudeInitMeta()
	meta["mcp_servers"] = servers
	meta["mcp_server_errors"] = errs
	meta["cwd"] = strings.Repeat("d", 4096)

	o.emitSessionInitSignal(context.Background(), sessionInitReq(), meta)

	e := cj.entries[0]
	if got := e.Payload["mcp_server_count"]; got != sessionInitListMax+7 {
		t.Errorf("mcp_server_count = %v, want the true total %d", got, sessionInitListMax+7)
	}
	kept, _ := e.Payload["mcp_servers"].([]map[string]any)
	if len(kept) != sessionInitListMax {
		t.Errorf("kept %d mcp_servers, want the list capped at %d", len(kept), sessionInitListMax)
	}
	if e.Payload["mcp_servers_truncated"] != true {
		t.Error("a cut list must say so")
	}
	keptErrs, _ := e.Payload["mcp_server_errors"].([]map[string]any)
	if len(keptErrs) != sessionInitListMax || e.Payload["mcp_server_errors_truncated"] != true {
		t.Errorf("skipped-server list not capped: %d entries, truncated=%v", len(keptErrs), e.Payload["mcp_server_errors_truncated"])
	}
	if cwd, _ := e.Payload["cwd"].(string); len(cwd) > sessionInitFieldMax+32 {
		t.Errorf("cwd not bounded: %d bytes", len(cwd))
	}
	if len(e.Summary) > 600 {
		t.Errorf("summary is a one-liner a human reads, got %d bytes", len(e.Summary))
	}
}

// End-to-end through the real Claude stream parser: the CLI can emit several
// system events per run (sub-agents, restarts, opencode re-reports its model on
// every step_finish), and this entry is a per-RUN record — one row, no matter
// how many init events arrive.
func TestRunAgent_SessionInitEntryEmittedOncePerRun(t *testing.T) {
	t.Parallel()
	const secret = "sk-ant-api03-NOTAREALKEYbutTREATITASONE"
	const initLine = `{"type":"system","subtype":"init","model":"claude-opus-4-8","tools":["Bash","Read"],` +
		`"cwd":"/workspace","mcp_servers":[{"name":"github","status":"connected"}],` +
		`"claude_code_version":"2.1.219","session_id":"sess-e2e","apiKeySource":"ANTHROPIC_API_KEY",` +
		`"permissionMode":"bypassPermissions","capabilities":["interrupt_receipt_v1"],` +
		`"mcp_server_errors":[{"name":"crewship-memory","type":"invalid_config","message":"token ` + secret + ` rejected"}]}`

	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: initLine + "\n" + initLine + "\n"}), newMemState(), covQuietLogger())
	o.SetJournal(j)
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("run.session_init")
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 run.session_init entry for the run, got %d", len(entries))
	}
	e := entries[0]
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want error — crewship-memory was skipped", e.Severity)
	}
	if e.Payload["mcp_server_error_count"] != 1 {
		t.Errorf("mcp_server_error_count = %v, want 1", e.Payload["mcp_server_error_count"])
	}
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	// The tap this is emitted from sits BEFORE the credential scrubber, so the
	// only defence is not copying free text in the first place.
	if strings.Contains(string(blob), secret) {
		t.Fatalf("secret from mcp_server_errors[].message reached the journal: %s", blob)
	}
	if !strings.Contains(string(blob), "crewship-memory") {
		t.Errorf("the skipped server must be named: %s", blob)
	}
}

// The entry hung off the resolved-model guard, which only fires when the init
// event carries a non-empty model. That coupled the alert to a field it does
// not need: an init reporting a SKIPPED MCP server and no model would have
// emitted nothing at all — silence in exactly the case the entry exists to
// break. Claude Code always sends model today, which is precisely why the hole
// would have gone unnoticed.
func TestRunAgent_SessionInitEntryDoesNotRequireAModel(t *testing.T) {
	t.Parallel()
	const initLine = `{"type":"system","subtype":"init","tools":["Bash"],` +
		`"claude_code_version":"2.1.219","session_id":"sess-nomodel",` +
		`"mcp_server_errors":[{"name":"crewship-memory","type":"invalid_config"}]}`

	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: initLine + "\n"}), newMemState(), covQuietLogger())
	o.SetJournal(j)
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("run.session_init")
	if len(entries) != 1 {
		t.Fatalf("want 1 run.session_init entry, got %d — a modelless init still opened a session", len(entries))
	}
	if got := entries[0].Severity; got != "error" {
		t.Errorf("Severity = %q, want error — a server was skipped", got)
	}
}

// A system event that is not an init (api_retry, thinking_tokens, the
// scrubber's own notice) must not be mistaken for one.
func TestRunAgent_SessionInitEntryIgnoresNonInitSystemEvents(t *testing.T) {
	t.Parallel()
	const stream = `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3,"error":"overloaded"}
{"type":"system","subtype":"init","model":"claude-opus-4-8","session_id":"sess-after-retry"}
`
	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	o.SetJournal(j)
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("run.session_init")
	if len(entries) != 1 {
		t.Fatalf("want 1 run.session_init entry, got %d", len(entries))
	}
	if got := entries[0].Payload["session_id"]; got != "sess-after-retry" {
		t.Errorf("session_id = %v, want sess-after-retry — the retry envelope must not take the slot", got)
	}
}

// The adapter passes mcp_server_errors through as unparsed JSON, so this code
// is the first thing that has an opinion about its shape. Two ways a shape it
// does not recognise turns a degraded session into a quiet one:
//
//   - the value does not decode into a list of objects at all, so `skipped` is
//     empty and the entry is written at info with the ordinary "session on
//     <model>" summary;
//   - it decodes, but every element projects to {} because the CLI renamed the
//     fields, so the alarm fires naming no server.
//
// Both are worse than a noisy entry: "the CLI told us something was skipped"
// is the fact, and it survives not understanding the details.
func TestEmitSessionInitSignal_UnexpectedSkipShapeStillRaises(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"object keyed by server name", `{"crewship-memory":{"type":"invalid_config"}}`},
		{"array of strings", `["crewship-memory"]`},
		{"array of objects with unknown keys", `[{"server":"crewship-memory","reason":"invalid_config"}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &covJournal{}
			o := New(nil, newMemState(), covQuietLogger())
			o.SetJournal(j)

			o.emitSessionInitSignal(context.Background(), covRunReq(), map[string]any{
				"subtype":           "init",
				"model":             "claude-opus-4-8",
				"mcp_server_errors": json.RawMessage(tc.raw),
			})

			entries := j.byType("run.session_init")
			if len(entries) != 1 {
				t.Fatalf("want 1 entry, got %d", len(entries))
			}
			if got := entries[0].Severity; got != "error" {
				t.Errorf("Severity = %q, want error — the CLI reported a skipped server, "+
					"and not understanding its shape is not a reason to call the session healthy", got)
			}
			sum := entries[0].Summary
			if !strings.Contains(strings.ToLower(sum), "skip") {
				t.Errorf("Summary = %q, want it to say something was skipped", sum)
			}
			// The summary is what an operator reads in the feed, so it must not
			// contradict its own severity or trail an empty list. "0 of 2 were
			// SKIPPED ()" is a line that makes the alert look broken and gets
			// the alert ignored.
			if strings.Contains(sum, "0 of") {
				t.Errorf("Summary = %q claims nothing was skipped while raising a degraded alert", sum)
			}
			if strings.Contains(sum, "()") {
				t.Errorf("Summary = %q trails an empty server list", sum)
			}
		})
	}
}

// An alarm that names no server is an alarm nobody can act on.
func TestEmitSessionInitSignal_NeverEmitsNamelessSkipEntries(t *testing.T) {
	t.Parallel()
	j := &covJournal{}
	o := New(nil, newMemState(), covQuietLogger())
	o.SetJournal(j)

	o.emitSessionInitSignal(context.Background(), covRunReq(), map[string]any{
		"subtype": "init",
		"model":   "claude-opus-4-8",
		// Recognisable as a list of objects, but carrying none of the keys we
		// project — the projection would otherwise store [{}].
		"mcp_server_errors": json.RawMessage(`[{"server":"crewship-memory"},{"server":"github"}]`),
	})

	entries := j.byType("run.session_init")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	blob, err := json.Marshal(entries[0].Payload["mcp_server_errors"])
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if strings.Contains(string(blob), "{}") {
		t.Errorf("payload carries empty skip objects (%s) — the count says two servers were "+
			"skipped and the list names neither", blob)
	}
	if entries[0].Payload["mcp_server_error_count"] != 2 {
		t.Errorf("mcp_server_error_count = %v, want 2 — the count is knowable even when the names are not",
			entries[0].Payload["mcp_server_error_count"])
	}
}

// The honest summary — "reported skipped MCP servers in a shape this build
// cannot read" — was added for exactly the shape below and could never run:
// every unreadable entry was padded to "(unnamed)", so the name list was never
// empty and the counted sentence always won. What an operator got was
// "1 of 1 configured MCP servers were SKIPPED ((unnamed))": a line that claims
// to name the lost server, names nothing, and reads as a broken alert.
func TestEmitSessionInitSignal_UnnameableSkipSaysSoInsteadOfPadding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"renamed keys", `[{"server":"crewship-memory","reason":"invalid_config"}]`},
		{"name typed as something else", `[{"name":{"id":"crewship-memory"},"type":7}]`},
		{"empty objects", `[{},{}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &covJournal{}
			o := New(nil, newMemState(), covQuietLogger())
			o.SetJournal(j)

			o.emitSessionInitSignal(context.Background(), covRunReq(), map[string]any{
				"subtype":           "init",
				"model":             "claude-opus-4-8",
				"mcp_server_errors": json.RawMessage(tc.raw),
			})

			entries := j.byType("run.session_init")
			if len(entries) != 1 {
				t.Fatalf("want 1 entry, got %d", len(entries))
			}
			sum := entries[0].Summary
			if strings.Contains(sum, "(unnamed)") {
				t.Errorf("Summary = %q — a placeholder standing in for a server nobody can name, "+
					"in the line that is supposed to tell an operator WHICH capability was lost", sum)
			}
			if !strings.Contains(sum, "cannot read") {
				t.Errorf("Summary = %q, want the honest 'shape this build cannot read' line", sum)
			}
			if entries[0].Severity != "error" {
				t.Errorf("Severity = %q, want error — the CLI still reported a skip", entries[0].Severity)
			}
		})
	}
}

// A summary that CAN name something still names it: the honest branch replaces
// padding, not reporting.
func TestEmitSessionInitSignal_PartiallyReadableSkipStillNames(t *testing.T) {
	t.Parallel()
	j := &covJournal{}
	o := New(nil, newMemState(), covQuietLogger())
	o.SetJournal(j)

	o.emitSessionInitSignal(context.Background(), covRunReq(), map[string]any{
		"subtype": "init",
		"model":   "claude-opus-4-8",
		// One entry we can read, one whose keys were renamed, and one carrying
		// only the failure category.
		"mcp_server_errors": json.RawMessage(
			`[{"name":"crewship-memory","type":"invalid_config"},{"server":"github"},{"type":"url_missing_type"}]`),
	})

	entries := j.byType("run.session_init")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	sum := entries[0].Summary
	for _, want := range []string{"crewship-memory", "invalid_config", "url_missing_type"} {
		if !strings.Contains(sum, want) {
			t.Errorf("Summary = %q, missing %q — everything readable belongs in the line", sum, want)
		}
	}
	if strings.Contains(sum, "(unnamed)") {
		t.Errorf("Summary = %q pads the entry it could not read", sum)
	}
	if strings.Contains(sum, "cannot read") {
		t.Errorf("Summary = %q claims it read nothing while naming two servers", sum)
	}
}

// "N of M configured MCP servers were SKIPPED" is the number an operator judges
// how degraded the session is by, and M was len(skipped)+len(mcp_servers). The
// CLI lists a server it failed to load in BOTH arrays — once under mcp_servers
// with a failed status, once under mcp_server_errors with the reason — so every
// such server was counted twice and the denominator grew by exactly the failures
// it was supposed to put in perspective: "1 of 4" for a session with three
// servers configured, or the absurd "2 of 2" reading as total loss when one of
// two servers loaded fine.
func TestEmitSessionInitSignal_DegradedDenominatorCountsEachServerOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		servers any
		skips   any
		want    string
	}{
		{
			// The CLI's actual shape: the failed server appears in both lists.
			name: "skipped server also listed under mcp_servers",
			servers: []json.RawMessage{
				json.RawMessage(`{"name":"crewship-memory","status":"failed"}`),
				json.RawMessage(`{"name":"linear","status":"connected"}`),
				json.RawMessage(`{"name":"sentry","status":"connected"}`),
			},
			skips: json.RawMessage(`[{"name":"crewship-memory","type":"invalid_config"}]`),
			want:  "1 of 3 configured",
		},
		{
			// Two of two configured, one skipped: the inflated denominator made
			// this "1 of 3" and understated the loss.
			name: "half the inventory lost",
			servers: []json.RawMessage{
				json.RawMessage(`{"name":"crewship-memory","status":"failed"}`),
				json.RawMessage(`{"name":"linear","status":"connected"}`),
			},
			skips: json.RawMessage(`[{"name":"crewship-memory","type":"invalid_config"}]`),
			want:  "1 of 2 configured",
		},
		{
			// A CLI that reports the skip ONLY in mcp_server_errors: the server
			// is genuinely absent from the inventory, so it still has to be added.
			name: "skipped server absent from mcp_servers",
			servers: []json.RawMessage{
				json.RawMessage(`{"name":"linear","status":"connected"}`),
			},
			skips: json.RawMessage(`[{"name":"crewship-memory","type":"invalid_config"}]`),
			want:  "1 of 2 configured",
		},
		{
			// No inventory reported at all — the skips are all we know about.
			name:    "no mcp_servers reported",
			servers: nil,
			skips:   json.RawMessage(`[{"name":"crewship-memory","type":"invalid_config"}]`),
			want:    "1 of 1 configured",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &covJournal{}
			o := New(nil, newMemState(), covQuietLogger())
			o.SetJournal(j)

			meta := map[string]any{
				"subtype":           "init",
				"model":             "claude-opus-4-8",
				"mcp_server_errors": tc.skips,
			}
			if tc.servers != nil {
				meta["mcp_servers"] = tc.servers
			}
			o.emitSessionInitSignal(context.Background(), covRunReq(), meta)

			entries := j.byType("run.session_init")
			if len(entries) != 1 {
				t.Fatalf("want 1 entry, got %d", len(entries))
			}
			if !strings.Contains(entries[0].Summary, tc.want) {
				t.Errorf("Summary = %q, want %q — the denominator is what an operator judges how "+
					"degraded the session is by", entries[0].Summary, tc.want)
			}
		})
	}
}

// A session where nothing was skipped must not be reported as degraded just
// because the adapter handed the (empty) list over already decoded. Severity
// error plus a DEGRADED summary for a healthy session is how an alert stops
// being read.
func TestEmitSessionInitSignal_DecodedEmptySkipListIsHealthy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   any
	}{
		{"decoded objects", []map[string]any{}},
		{"raw JSON elements", []json.RawMessage{}},
		{"strings", []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &covJournal{}
			o := New(nil, newMemState(), covQuietLogger())
			o.SetJournal(j)

			o.emitSessionInitSignal(context.Background(), covRunReq(), map[string]any{
				"subtype":           "init",
				"model":             "claude-opus-4-8",
				"mcp_server_errors": tc.in,
			})

			entries := j.byType("run.session_init")
			if len(entries) != 1 {
				t.Fatalf("want 1 entry, got %d", len(entries))
			}
			if got := entries[0].Severity; got != "info" {
				t.Errorf("Severity = %q, want info — the CLI skipped nothing", got)
			}
			if strings.Contains(entries[0].Summary, "DEGRADED") {
				t.Errorf("Summary = %q calls a healthy session degraded", entries[0].Summary)
			}
			if v, ok := entries[0].Payload["mcp_server_error_count"]; ok {
				t.Errorf("mcp_server_error_count = %v for an empty list", v)
			}
		})
	}
}
