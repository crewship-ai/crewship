package manifest

import (
	"context"
	"strings"
	"testing"
)

// agents.suggested_prompts shipped as a column, an API field and a CLI
// flag while internal/manifest knew nothing about it. The symptom was
// silent: `crewship export` wrote a manifest with no prompts in it and
// `crewship apply` recreated the agent without them, so the documented
// way to move a workspace destroyed every configured chip.
//
// These tests pin the whole path — spec field, exporter, body builder,
// diff, and the caps the server enforces — and each of them fails
// against the pre-fix package.

const (
	// Two lines, so the block-scalar rendering is exercised rather than
	// a single-line string that YAML would quote inline.
	twoPrompts = "What shipped this week?\nWho is blocked?"
)

// ── exporter ────────────────────────────────────────────────────────────────

// suggestedPromptsStub wires one crew with one agent whose
// suggested_prompts column holds `promptsJSON` verbatim (pass "null" for
// an unconfigured agent).
func suggestedPromptsStub(promptsJSON string) *covStubAPI {
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[
		{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE",
		 "tool_profile":"CODING","timeout_seconds":1800,"memory_enabled":true,
		 "system_prompt":"hi amy","suggested_prompts":`+promptsJSON+`}
	]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	return stub
}

func TestExportCrew_CarriesSuggestedPrompts(t *testing.T) {
	cases := []struct {
		name        string
		promptsJSON string
		want        string
		wantInYAML  bool
	}{
		{"configured", `"What shipped this week?\nWho is blocked?"`, twoPrompts, true},
		// NULL column and empty string are the same "not configured"
		// state; neither may put an empty key in the manifest.
		{"null column", `null`, "", false},
		{"empty string", `""`, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportCrew(context.Background(),
				NewClient(suggestedPromptsStub(tc.promptsJSON)), "ops", DefaultExportOptions())
			if err != nil {
				t.Fatalf("ExportCrew: %v", err)
			}
			if got := strings.Contains(out, "suggested_prompts"); got != tc.wantInYAML {
				t.Errorf("suggested_prompts present in YAML = %v, want %v\n%s", got, tc.wantInYAML, out)
			}

			b, err := Load([]byte(out))
			if err != nil {
				t.Fatalf("reload exported YAML: %v\n%s", err, out)
			}
			agents := b.Documents[0].Spec.Agents
			if len(agents) != 1 {
				t.Fatalf("want 1 agent, got %d", len(agents))
			}
			if agents[0].SuggestedPrompts != tc.want {
				t.Errorf("round-tripped suggested_prompts = %q, want %q", agents[0].SuggestedPrompts, tc.want)
			}
		})
	}
}

// ── body builder ────────────────────────────────────────────────────────────

func TestBuildAgentBody_SuggestedPrompts(t *testing.T) {
	cases := []struct {
		name    string
		prompts string
		wantKey bool
	}{
		{"configured", twoPrompts, true},
		// Absent means "not declared here". A key with an empty value
		// would be read by the update handler as "clear the column".
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildAgentBody(&Agent{Slug: "amy", Name: "Amy", SuggestedPrompts: tc.prompts}, "c1", "ops")
			got, ok := body["suggested_prompts"]
			if ok != tc.wantKey {
				t.Fatalf("suggested_prompts in body = %v, want %v (body=%v)", ok, tc.wantKey, body)
			}
			if ok && got != tc.prompts {
				t.Errorf("suggested_prompts = %q, want %q", got, tc.prompts)
			}
		})
	}
}

// ── diff ────────────────────────────────────────────────────────────────────

func TestAgentBodyDiffers_SuggestedPrompts(t *testing.T) {
	strp := func(s string) *string { return &s }

	cases := []struct {
		name     string
		existing *string
		declared string
		want     bool
	}{
		{"unchanged", strp(twoPrompts), twoPrompts, false},
		{"changed", strp(twoPrompts), "What shipped this week?", true},
		{"newly declared over an unset column", nil, twoPrompts, true},
		// The round-trip case. An agent with no prompts exports with the
		// field omitted; re-planning that manifest must report unchanged
		// rather than an update that would never converge.
		{"both empty", nil, "", false},
		{"empty column, empty declaration", strp(""), "", false},
		// Omitting the field does not clear a server-side value — same
		// rule role_title follows.
		{"declared empty over a set column", strp(twoPrompts), "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := &AgentResponse{
				Name: "Amy", AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE",
				ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true,
				SuggestedPrompts: tc.existing,
			}
			declared := &Agent{
				Name: "Amy", AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE",
				ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true,
				SuggestedPrompts: tc.declared,
			}
			if got := agentBodyDiffers(existing, declared); got != tc.want {
				t.Errorf("agentBodyDiffers = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── apply ───────────────────────────────────────────────────────────────────

// findAgentItem returns the single plan item for kind "agent".
func findAgentItem(t *testing.T, p *Plan) *PlanItem {
	t.Helper()
	var found *PlanItem
	for i := range p.Items {
		if p.Items[i].Kind == "agent" {
			if found != nil {
				t.Fatalf("more than one agent item: %+v", p.Items)
			}
			found = &p.Items[i]
		}
	}
	if found == nil {
		t.Fatalf("no agent item in plan: %+v", p.Items)
	}
	return found
}

// bodyOf returns the recorded request body of the last call matching
// method+path.
func bodyOf(t *testing.T, stub *covStubAPI, method, path string) map[string]any {
	t.Helper()
	for i := len(stub.calls) - 1; i >= 0; i-- {
		if stub.calls[i].Method == method && stub.calls[i].Path == path {
			return stub.calls[i].Body
		}
	}
	t.Fatalf("no %s %s call recorded; calls=%+v", method, path, stub.calls)
	return nil
}

const promptsCrewYAML = `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  agents:
    - slug: amy
      name: Amy
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      tool_profile: CODING
      timeout_seconds: 1800
      memory_enabled: true
      prompt: hi amy
      suggested_prompts: |-
        What shipped this week?
        Who is blocked?
`

// A new agent gets its prompts through a follow-up PATCH, because
// POST /api/v1/agents does not model the column and drops it with a 201.
func TestApplyAgent_SuggestedPrompts_OnCreate(t *testing.T) {
	b, err := Load([]byte(promptsCrewYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)
	stub.on("POST", "/api/v1/agents", 201, `{"id":"a9","slug":"amy"}`)
	stub.on("PATCH", "/api/v1/agents/a9", 200, `{"id":"a9","slug":"amy"}`)

	plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	item := findAgentItem(t, plan)
	if item.Action != ActionCreate {
		t.Fatalf("action = %v, want create", item.Action)
	}
	if err := item.exec(context.Background(), NewClient(stub), Options{}); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if got := bodyOf(t, stub, "PATCH", "/api/v1/agents/a9")["suggested_prompts"]; got != twoPrompts {
		t.Errorf("follow-up PATCH suggested_prompts = %q, want %q", got, twoPrompts)
	}
}

// An existing agent whose column drifted is brought back by the ordinary
// update PATCH.
func TestApplyAgent_SuggestedPrompts_OnUpdate(t *testing.T) {
	b, err := Load([]byte(promptsCrewYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[
		{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE",
		 "tool_profile":"CODING","timeout_seconds":1800,"memory_enabled":true,
		 "system_prompt":"hi amy","suggested_prompts":"Something else"}
	]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
	stub.on("PATCH", "/api/v1/agents/a1", 200, `{"id":"a1","slug":"amy"}`)

	plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	item := findAgentItem(t, plan)
	if item.Action != ActionUpdate {
		t.Fatalf("action = %v, want update (drifted suggested_prompts must be detected)", item.Action)
	}
	if err := item.exec(context.Background(), NewClient(stub), Options{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := bodyOf(t, stub, "PATCH", "/api/v1/agents/a1")["suggested_prompts"]; got != twoPrompts {
		t.Errorf("update PATCH suggested_prompts = %q, want %q", got, twoPrompts)
	}
}

// The whole point: export then re-apply against the same server is a
// no-op, and the prompts survive it byte-for-byte. The empty case is
// here too — it is the one that would otherwise turn into a permanent
// phantom "1 to update".
func TestExportApplyRoundTrip_SuggestedPrompts(t *testing.T) {
	cases := []struct {
		name        string
		promptsJSON string
		want        string
	}{
		{"configured", `"What shipped this week?\nWho is blocked?"`, twoPrompts},
		{"not configured", `null`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := suggestedPromptsStub(tc.promptsJSON)

			out, err := ExportCrew(context.Background(), NewClient(stub), "ops", DefaultExportOptions())
			if err != nil {
				t.Fatalf("ExportCrew: %v", err)
			}
			b, err := Load([]byte(out))
			if err != nil {
				t.Fatalf("reload exported YAML: %v\n%s", err, out)
			}
			if got := b.Documents[0].Spec.Agents[0].SuggestedPrompts; got != tc.want {
				t.Fatalf("exported suggested_prompts = %q, want %q", got, tc.want)
			}

			plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if got := findAgentItem(t, plan).Action; got != ActionUnchanged {
				t.Errorf("re-applying an export reports %v, want unchanged — the round-trip drifts", got)
			}
		})
	}
}

// ── validation ──────────────────────────────────────────────────────────────

func TestValidate_SuggestedPromptsCaps(t *testing.T) {
	lines := func(n int, body string) string {
		out := make([]string, n)
		for i := range out {
			out[i] = body
		}
		return strings.Join(out, "\n")
	}

	cases := []struct {
		name    string
		prompts string
		wantErr string // "" = must validate
	}{
		{"unset", "", ""},
		{"one", "What shipped?", ""},
		{"exactly eight", lines(8, "ok"), ""},
		{"nine", lines(9, "ok"), "at most 8 are allowed"},
		// Blank lines are spacing in a textarea, not prompts: nine
		// separated by blanks is still nine, but eight is still eight.
		{"eight with blank lines between", strings.Join([]string{lines(8, "ok"), ""}, "\n\n"), ""},
		{"exactly 120 characters", strings.Repeat("a", 120), ""},
		{"121 characters", strings.Repeat("a", 121), "exceeds 120 characters"},
		// Runes, not bytes — 120 Czech characters is 120 characters.
		{"120 multibyte characters", strings.Repeat("ř", 120), ""},
		{"121 multibyte characters", strings.Repeat("ř", 121), "exceeds 120 characters"},
		// Position is counted over visible prompts, so the message names
		// the second one even though a blank line precedes it.
		{"names the offending position", "ok\n\n" + strings.Repeat("a", 121), "entry 2 exceeds"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bundle{Documents: []Document{{
				APIVersion: "crewship/v1",
				Kind:       "Crew",
				Metadata:   Metadata{Name: "Ops", Slug: "ops"},
				Spec: &CrewSpec{Agents: []Agent{{
					Slug: "amy", Name: "Amy", Prompt: "hi",
					SuggestedPrompts: tc.prompts,
				}}},
			}}}
			err := b.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}
