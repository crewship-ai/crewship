package manifest

import (
	"context"
	"strings"
	"testing"
)

// ask_forms is an update-only agent column. These tests exercise the whole
// user-facing contract: export must retain it, apply must PATCH it after a
// create, drift must be repaired, and an exported empty value must converge.
const oneAskForm = `[{"id":"receipt","label":"Add a receipt","template":"Supplier: {{supplier}}","attachment":"required","fields":[{"name":"supplier","label":"Supplier","type":"text","required":true}]}]`

func askFormsStub(formsJSON string) *covStubAPI {
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[
		{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE",
		 "tool_profile":"CODING","timeout_seconds":1800,"memory_enabled":true,
		 "system_prompt":"hi amy","ask_forms":`+formsJSON+`}
	]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	return stub
}

func TestExportApplyRoundTrip_AskForms(t *testing.T) {
	tests := []struct {
		name       string
		formsJSON  string
		want       string
		wantInYAML bool
	}{
		{"configured", `"` + strings.ReplaceAll(oneAskForm, `"`, `\"`) + `"`, oneAskForm, true},
		{"null column", `null`, "", false},
		{"empty column", `""`, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := askFormsStub(tt.formsJSON)
			out, err := ExportCrew(context.Background(), NewClient(stub), "ops", DefaultExportOptions())
			if err != nil {
				t.Fatalf("ExportCrew: %v", err)
			}
			if got := strings.Contains(out, "ask_forms:"); got != tt.wantInYAML {
				t.Fatalf("ask_forms present in YAML = %v, want %v\n%s", got, tt.wantInYAML, out)
			}
			bundle, err := Load([]byte(out))
			if err != nil {
				t.Fatalf("reload exported YAML: %v\n%s", err, out)
			}
			if got := bundle.Documents[0].Spec.Agents[0].AskForms; got != tt.want {
				t.Fatalf("round-tripped ask_forms = %q, want %q", got, tt.want)
			}
			plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{Mode: ApplyUpsert})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if got := findAgentItem(t, plan).Action; got != ActionUnchanged {
				t.Errorf("re-applying export reports %v, want unchanged", got)
			}
		})
	}
}

const askFormsCrewYAML = `
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
      ask_forms: >-
        [{"id":"receipt","label":"Add a receipt","template":"Supplier: {{supplier}}","attachment":"required","fields":[{"name":"supplier","label":"Supplier","type":"text","required":true}]}]
`

func TestApplyAgent_AskForms_CreateAndUpdate(t *testing.T) {
	for _, tt := range []struct {
		name       string
		existing   string
		wantAction Action
		method     string
		path       string
	}{
		{"create", `[]`, ActionCreate, "PATCH", "/api/v1/agents/a9"},
		{"update drift", `[{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","tool_profile":"CODING","timeout_seconds":1800,"memory_enabled":true,"system_prompt":"hi amy","ask_forms":"[]"}]`, ActionUpdate, "PATCH", "/api/v1/agents/a1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := Load([]byte(askFormsCrewYAML))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			stub := newCovStub()
			stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
			stub.on("GET", "/api/v1/credentials", 200, `[]`)
			stub.on("GET", "/api/v1/agents?crew_id=c1", 200, tt.existing)
			stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
			stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)
			stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
			stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
			stub.on("POST", "/api/v1/agents", 201, `{"id":"a9","slug":"amy"}`)
			stub.on("PATCH", tt.path, 200, `{"id":"a9","slug":"amy"}`)

			plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{Mode: ApplyUpsert})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			item := findAgentItem(t, plan)
			if item.Action != tt.wantAction {
				t.Fatalf("action = %v, want %v", item.Action, tt.wantAction)
			}
			if err := item.exec(context.Background(), NewClient(stub), Options{}); err != nil {
				t.Fatalf("exec: %v", err)
			}
			if got := bodyOf(t, stub, tt.method, tt.path)["ask_forms"]; got != oneAskForm {
				t.Errorf("ask_forms PATCH = %q, want byte-for-byte %q", got, oneAskForm)
			}
		})
	}
}

func TestValidate_AskFormsAtManifestBoundary(t *testing.T) {
	bad := strings.Replace(oneAskForm, "{{supplier}}", "{{suplier}}", 1)
	bundle := &Bundle{Documents: []Document{{
		APIVersion: APIVersion,
		Kind:       "Crew",
		Metadata:   Metadata{Name: "Ops", Slug: "ops"},
		Spec:       &CrewSpec{Agents: []Agent{{Slug: "amy", Name: "Amy", AgentRole: "LEAD", AskForms: bad}}},
	}}}
	err := bundle.Validate()
	if err == nil || !strings.Contains(err.Error(), "{{suplier}}") {
		t.Fatalf("want the broken placeholder rejected before apply, got %v", err)
	}
}
