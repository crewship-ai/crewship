package manifest

import (
	"context"
	"strings"
	"testing"
)

// Regression tests for #2426 at the dispatcher level: a standalone
// Project that names a lead which exists only on the server, and a
// Label applied twice.

// TestBuildPlan_StandaloneProjectLeadResolvesRemoteAgent: the file
// declares no agents; `mia` lives on the server. Validation must
// accept the lead (the planner lists the workspace's agents into
// RemoteAgents) and the create body must carry the resolved lead_id.
func TestBuildPlan_StandaloneProjectLeadResolvesRemoteAgent(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Demo, slug: demo-v2 }
spec: { status: in_progress, lead_agent_slug: mia }
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api := newKindsFakeAPI()
	api.agents = []map[string]any{{"id": "agent_mia", "slug": "mia", "name": "Mia"}}

	res, err := Apply(context.Background(), NewClient(api), bundle, Options{Yes: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("Created = %d, want 1 (LastError=%v)", res.Created, res.LastError)
	}
	var post map[string]any
	for _, c := range api.Calls {
		if c.Method == "POST" && c.Path == "/api/v1/projects" {
			post = c.Body
		}
	}
	if post == nil {
		t.Fatalf("no POST /api/v1/projects; calls=%+v", api.Calls)
	}
	if got, _ := post["lead_id"].(string); got != "agent_mia" {
		t.Errorf("lead_id = %q, want agent_mia", got)
	}
	if got, _ := post["lead_type"].(string); got != "agent" {
		t.Errorf("lead_type = %q, want agent", got)
	}
}

// TestBuildPlan_StandaloneProjectLeadUnknownFailsAtValidate: with the
// workspace's agents fetched, a lead that exists nowhere is still a
// validation error — and it is reported before anything is written.
func TestBuildPlan_StandaloneProjectLeadUnknownFailsAtValidate(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Demo, slug: demo-v2 }
spec: { lead_agent_slug: ghost }
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api := newKindsFakeAPI()
	api.agents = []map[string]any{{"id": "agent_mia", "slug": "mia", "name": "Mia"}}

	_, err = BuildPlan(context.Background(), NewClient(api), bundle, Options{})
	if err == nil || !strings.Contains(err.Error(), "lead_agent_slug") || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want a validation error naming the lead, got %v", err)
	}
	for _, c := range api.Calls {
		if c.Method != "GET" {
			t.Fatalf("validation failure must not write; saw %s %s", c.Method, c.Path)
		}
	}
}

// TestBuildPlan_LabelIsIdempotent: the second apply of the same Label
// must plan Unchanged (or Update on drift), never a second create —
// the server answers that with 409 and the whole file stops there.
func TestBuildPlan_LabelIsIdempotent(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Label
metadata: { name: demo-v2, slug: demo-v2 }
spec: { color: "#EF4444" }
`)
	cases := []struct {
		name       string
		remote     []map[string]any
		wantAction Action
		wantMethod string
	}{
		{"absent creates", nil, ActionCreate, "POST"},
		{"present and equal is unchanged", []map[string]any{
			{"id": "lbl_1", "name": "demo-v2", "color": "#EF4444"},
		}, ActionUnchanged, ""},
		{"present with drift updates", []map[string]any{
			{"id": "lbl_1", "name": "demo-v2", "color": "#000000"},
		}, ActionUpdate, "PATCH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := Load(body)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			api := newKindsFakeAPI()
			api.labels = tc.remote

			plan, err := BuildPlan(context.Background(), NewClient(api), bundle, Options{})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if len(plan.Items) != 1 {
				t.Fatalf("want 1 item, got %d: %+v", len(plan.Items), plan.Items)
			}
			if plan.Items[0].Action != tc.wantAction {
				t.Fatalf("action = %v, want %v (%s)", plan.Items[0].Action, tc.wantAction, plan.Items[0].Description)
			}

			if _, err := Apply(context.Background(), NewClient(api), bundle, Options{Yes: true}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			var writes []string
			for _, c := range api.Calls {
				if c.Method != "GET" {
					writes = append(writes, c.Method+" "+c.Path)
				}
			}
			switch {
			case tc.wantMethod == "" && len(writes) != 0:
				t.Fatalf("unchanged label must not write; saw %v", writes)
			case tc.wantMethod != "" && (len(writes) != 1 || !strings.HasPrefix(writes[0], tc.wantMethod+" /api/v1/labels")):
				t.Fatalf("want one %s /api/v1/labels write, saw %v", tc.wantMethod, writes)
			}
		})
	}
}

// TestBundleReferencesAgents pins that every kind whose Validate calls
// ctx.HasAgent triggers the workspace agent fetch — Routine, RecurringIssue
// and TriageRule check the slug unconditionally, so a bundle holding only
// one of them would otherwise reject an agent from an earlier apply.
func TestBundleReferencesAgents(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"label only", `
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#ff0000" }
`, false},
		{"project lead", `
apiVersion: crewship/v1
kind: Project
metadata: { name: Demo, slug: demo }
spec: { status: planned, lead_agent_slug: mia }
`, true},
		{"routine agent_run step", `
apiVersion: crewship/v1
kind: Routine
metadata: { name: Nightly, slug: nightly }
spec:
  crew_slug: ops
  steps:
    - { id: run, type: agent_run, agent_slug: mia }
`, true},
		{"recurring issue assignee", `
apiVersion: crewship/v1
kind: RecurringIssue
metadata: { name: Weekly, slug: weekly }
spec:
  schedule: "0 9 * * 1"
  template: { title: Weekly review, crew_slug: ops, assignee_agent_slug: mia }
`, true},
		{"triage rule assign_to", `
apiVersion: crewship/v1
kind: TriageRule
metadata: { name: Route bugs, slug: route-bugs }
spec:
  match: { title_contains: [bug] }
  actions: { assign_to_agent_slug: mia }
`, true},
		{"triage rule from_agent", `
apiVersion: crewship/v1
kind: TriageRule
metadata: { name: From mia, slug: from-mia }
spec:
  match: { from_agent_slug: mia }
  actions: { add_labels: [triaged] }
`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := Load([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := bundleReferencesAgents(bundle); got != tc.want {
				t.Errorf("bundleReferencesAgents = %v, want %v", got, tc.want)
			}
		})
	}
}
