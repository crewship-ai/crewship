package manifest

import (
	"context"
	"strings"
	"testing"
)

// A crew-inherited credential row is a delivery fact, not a declaration.
// A device-monitoring dry-run planned `agent_credential` deletes for every
// crew_link row the server listed; --no-delete was the only thing that
// stopped a no-op "delete" being reported as a change. Re-applying the
// same manifest must plan nothing for inherited rows.
func TestBuildPlan_InheritedCredentialRowsAreNeverDeleted(t *testing.T) {
	ctx := context.Background()
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, cli_adapter: CLAUDE_CODE, tool_profile: CODING, timeout_seconds: 1800, prompt: x }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200,
		`[{"id":"a1","slug":"a","name":"A","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","tool_profile":"CODING","timeout_seconds":1800,"system_prompt":"x"}]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[
		{"id":"","agent_id":"a1","credential_id":"crCrew","credential_name":"CREW_TOKEN","env_var_name":"CREW_TOKEN","grant_source":"crew"},
		{"id":"bindOld","agent_id":"a1","credential_id":"crOld","credential_name":"OLD","env_var_name":"OLD","grant_source":"explicit"}
	]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)

	plan, err := BuildPlan(ctx, NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var deletes []string
	for _, it := range plan.Items {
		if it.Kind == "agent_credential" && it.Action == ActionDelete {
			deletes = append(deletes, it.Description)
		}
	}
	if len(deletes) != 1 || !strings.Contains(deletes[0], "OLD") {
		t.Fatalf("deletes = %v, want only the explicit OLD assignment (never the crew-inherited CREW_TOKEN)", deletes)
	}
}

func TestAgentCredentialBinding_Explicit(t *testing.T) {
	for _, tc := range []struct {
		b    AgentCredentialBinding
		want bool
	}{
		{AgentCredentialBinding{ID: "x", GrantSource: "explicit"}, true},
		{AgentCredentialBinding{ID: "x"}, true}, // older server, real row
		{AgentCredentialBinding{ID: "", GrantSource: "crew"}, false},
		{AgentCredentialBinding{ID: ""}, false}, // older server, inherited row
		{AgentCredentialBinding{ID: "x", GrantSource: "binding"}, false},
	} {
		if got := tc.b.Explicit(); got != tc.want {
			t.Errorf("%+v.Explicit() = %v, want %v", tc.b, got, tc.want)
		}
	}
}

func TestRemoveCredentialFromAgent_RefusesEmptyAssignmentID(t *testing.T) {
	stub := newCovStub()
	err := NewClient(stub).RemoveCredentialFromAgent(context.Background(), "a1", "")
	if err == nil || !strings.Contains(err.Error(), "no assignment id") {
		t.Fatalf("err = %v, want refusal instead of a 404-as-success", err)
	}
	if stub.countCalls("DELETE", "/api/v1/agents/a1/credentials/") != 0 {
		t.Fatal("an empty-id DELETE was sent")
	}
}
