package manifest

import (
	"context"
	"strings"
	"testing"
)

// A Composio grant with no user_id was planned as a CREATE, executed, refused
// by the server ("user_id is required"), downgraded to a warning, and reported
// as applied. So every run of the demo manifest printed
//
//	+ composio_grant demo-riley:gmail
//	Applied: 1 created
//
// for a grant that did not exist before and did not exist after. The plan
// never settled, and "apply until it says nothing changed" is the contract.
//
// The condition is knowable at plan time — the bind endpoint requires the id —
// so this is not something to discover by trying. It joins the same
// declared-but-not-applicable list a channel with no webhook URL already used,
// rather than getting a second parallel mechanism that prints the same idea in
// a different shape.

func TestPlan_UnappliableComposioGrantIsSkippedNotCreated(t *testing.T) {
	b := loadDemoManifest(t)
	api := newFakeAPI(t)
	api.agentsBySlug["demo-riley"] = map[string]any{"id": "ag_1", "slug": "demo-riley"}

	plan, err := BuildPlan(context.Background(), NewClient(api), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if item := findPlanItem(plan, "composio_grant", "demo-riley:gmail"); item != nil &&
		item.Action == ActionCreate {
		t.Errorf("a grant that cannot be applied must not plan a create: %s", item.Description)
	}

	var found bool
	for _, s := range plan.Skipped {
		if strings.Contains(s, "gmail") && strings.Contains(s, "user_id") {
			found = true
		}
	}
	if !found {
		t.Errorf("the grant must be reported as skipped and say what is missing, got %v", plan.Skipped)
	}
}

func TestPlan_SkippedCoversChannelsAndGrantsTogether(t *testing.T) {
	// One list, one heading. Both are the same fact — "declared, not
	// applied, here is the thing you have to supply" — and a reader
	// scanning output should not have to learn two places to look.
	b := loadDemoManifest(t)
	plan, err := BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var sawChannel, sawGrant bool
	for _, s := range plan.Skipped {
		if strings.Contains(s, "DISCORD_WEBHOOK_URL") {
			sawChannel = true
		}
		if strings.Contains(s, "gmail") {
			sawGrant = true
		}
	}
	if !sawChannel || !sawGrant {
		t.Errorf("want both the channel and the grant reported, got %v", plan.Skipped)
	}
}

func TestPlan_GrantWithAUserIDStillPlansACreate(t *testing.T) {
	// The skip must be about the missing id, not about Composio grants in
	// general — a grant that CAN be applied still has to be.
	b := loadDemoManifest(t)
	b.Workspaces[0].Spec.Crews[0].Agents[0].ComposioToolkits[0].UserID = "usr_connected"

	plan, err := BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	item := findPlanItem(plan, "composio_grant", "demo-riley:gmail")
	if item == nil || item.Action != ActionCreate {
		t.Fatalf("want a create for an appliable grant, got %+v", item)
	}
}
