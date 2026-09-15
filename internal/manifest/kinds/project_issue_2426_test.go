package kinds

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// Regression tests for #2426 — the four manifest defects found by
// applying a standalone Project + Label + Issue file.

// TestProject_Validate_StatusVocabularyMatchesAPI pins the Project
// status allow-list to the words the API and the projects.status
// CHECK constraint accept (internal/database/migrate_consts_v33_v41.go,
// migration v40). The manifest used to accept `active` and `archived`,
// which the server answered with a 500 at apply time.
func TestProject_Validate_StatusVocabularyMatchesAPI(t *testing.T) {
	cases := []struct {
		status string
		ok     bool
	}{
		{"backlog", true},
		{"planned", true},
		{"in_progress", true},
		{"paused", true},
		{"completed", true},
		{"cancelled", true},
		{"active", false},
		{"archived", false},
		{"IN_PROGRESS", false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			d := projectValidDoc()
			d.Spec.Status = tc.status
			err := d.Validate(projectValidCtx())
			if tc.ok && err != nil {
				t.Fatalf("status %q should validate, got %v", tc.status, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("status %q should be rejected", tc.status)
				}
				if !strings.Contains(err.Error(), "invalid status") || !strings.Contains(err.Error(), "in_progress") {
					t.Fatalf("error should name the field and list the API vocabulary, got %v", err)
				}
			}
		})
	}
}

// TestProject_Validate_PriorityAcceptsServerDefault: the server stores
// priority `none` by default and Export round-trips it, so a manifest
// exported from a fresh project must validate again.
func TestProject_Validate_PriorityAcceptsServerDefault(t *testing.T) {
	d := projectValidDoc()
	d.Spec.Priority = "none"
	if err := d.Validate(projectValidCtx()); err != nil {
		t.Fatalf("priority none should validate, got %v", err)
	}
}

// TestProject_Validate_LeadAgentDefersWhenNoAgentsKnown: a standalone
// Project file declares no agents and the client-less validation path
// (ValidateBundle, the in-container manifest tool) never fetches the
// workspace's agents. Rejecting the lead there was the #2426 defect;
// like Issue.assignee_slug, the reference is resolved at plan time.
func TestProject_Validate_LeadAgentDefersWhenNoAgentsKnown(t *testing.T) {
	d := projectValidDoc()
	d.Spec.LeadAgentSlug = "mia"
	if err := d.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("lead on a standalone project must defer to plan time, got %v", err)
	}
}

// TestProject_Validate_LeadAgentRejectedWhenAgentsKnown: once the
// context carries agents (declared or fetched from the workspace), a
// lead that is in neither list is still a validation error.
func TestProject_Validate_LeadAgentRejectedWhenAgentsKnown(t *testing.T) {
	cases := []struct {
		name string
		ctx  internalapi.WorkspaceContext
	}{
		{"declared only", internalapi.WorkspaceContext{
			DeclaredAgents: []internalapi.SlugLookup{{Slug: "pepa"}},
		}},
		{"remote only", internalapi.WorkspaceContext{
			RemoteAgents: []internalapi.SlugLookup{{Slug: "pepa"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := projectValidDoc()
			d.Spec.LeadAgentSlug = "ghost"
			err := d.Validate(tc.ctx)
			if err == nil || !strings.Contains(err.Error(), "lead_agent_slug") {
				t.Fatalf("want lead_agent_slug error, got %v", err)
			}
		})
	}
}

// TestListAgentSlugs feeds WorkspaceContext.RemoteAgents: the planner
// calls it so a Project lead or Issue assignee that lives only on the
// server passes validation.
func TestListAgentSlugs(t *testing.T) {
	c := newCovClient(map[string]covRoute{
		"GET /api/v1/agents": {body: `[{"id":"a1","slug":"mia","name":"Mia"},{"id":"a2","slug":"pepa","name":"Pepa"}]`},
	})
	got, err := ListAgentSlugs(context.Background(), c)
	if err != nil {
		t.Fatalf("ListAgentSlugs: %v", err)
	}
	if len(got) != 2 || got[0].Slug != "mia" || got[0].Name != "Mia" || got[1].Slug != "pepa" {
		t.Fatalf("got %+v", got)
	}

	empty := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[]`}})
	got, err = ListAgentSlugs(context.Background(), empty)
	if err != nil {
		t.Fatalf("ListAgentSlugs(empty): %v", err)
	}
	if got == nil {
		t.Fatal("an empty workspace must yield a non-nil slice so KnowsAgents stays truthful")
	}
}

// TestLookupLabelRemoteByName: the label planner used to pass a nil
// remote unconditionally, so every apply planned a create and the
// second one died on 409.
func TestLookupLabelRemoteByName(t *testing.T) {
	c := newCovClient(map[string]covRoute{
		"GET /api/v1/labels": {body: `[{"id":"l1","name":"bug","color":"#EF4444"},{"id":"l2","name":"demo-v2","color":"#000000"}]`},
	})
	got, err := LookupLabelRemoteByName(context.Background(), c, "demo-v2")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || got.ID != "l2" || got.Color != "#000000" {
		t.Fatalf("got %+v", got)
	}
	missing, err := LookupLabelRemoteByName(context.Background(), c, "nope")
	if err != nil || missing != nil {
		t.Fatalf("missing label: got %+v err=%v", missing, err)
	}

	broken := newCovClient(map[string]covRoute{"GET /api/v1/labels": {status: 500, body: `{"error":"boom"}`}})
	if _, err := LookupLabelRemoteByName(context.Background(), broken, "bug"); err == nil {
		t.Fatal("a failed list must surface, not read as 'label absent'")
	}
}

// TestIssue_Plan_CreateCarriesStatus: `status: todo` in the manifest
// must reach the create body in the server's canonical form, so the
// row lands as TODO rather than the server default BACKLOG.
func TestIssue_Plan_CreateCarriesStatus(t *testing.T) {
	cases := []struct {
		declared string
		want     string
	}{
		{"todo", "TODO"},
		{"IN_PROGRESS", "IN_PROGRESS"},
	}
	for _, tc := range cases {
		t.Run(tc.declared, func(t *testing.T) {
			doc := issueSampleDoc()
			doc.Spec.Status = tc.declared
			client := newIssueFake()
			issueSeedFakeFull(client)

			items, err := doc.Plan(context.Background(), client, nil)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if err := items[0].Exec(context.Background(), client); err != nil {
				t.Fatalf("Exec: %v", err)
			}
			body, _ := client.findCall("POST", "/api/v1/crews/crew_eng/issues").Body.(map[string]any)
			if got, _ := body["status"].(string); got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unset stays omitted", func(t *testing.T) {
		doc := issueSampleDoc()
		doc.Spec.Status = ""
		client := newIssueFake()
		issueSeedFakeFull(client)
		items, err := doc.Plan(context.Background(), client, nil)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if err := items[0].Exec(context.Background(), client); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		body, _ := client.findCall("POST", "/api/v1/crews/crew_eng/issues").Body.(map[string]any)
		if _, has := body["status"]; has {
			t.Errorf("body should not carry status when unset, got %v", body["status"])
		}
	})
}
