package manifest

import (
	"context"
	"testing"
)

// Routines were re-saved on EVERY apply.
//
// The routine kind has drift detection — RoutineRemote, routineDiffers,
// listRoutines, and a Plan branch that reports "unchanged" — but the phase
// that dispatches routines called `doc.Plan(ctx, c, nil)`, passing nil for the
// remote unconditionally. With no remote there is nothing to compare against,
// so every routine planned as a create and every apply wrote a new version of
// a routine nobody had touched.
//
// It went unnoticed because a plan line saying "create routine x" on a routine
// that already exists still succeeds — save is an upsert. The cost is silent:
// version churn, a plan that always shows work to do, and a manifest that can
// never honestly report "nothing changed".
//
// Issues do this correctly (Phase 14.5 looks up the remote first); routines
// now match.

func TestRoutinePhase_ReportsUnchangedWhenTheRemoteMatches(t *testing.T) {
	// Plan-level, not a round trip: the bug is that the routine phase never
	// LOOKED for a remote, so it could never say "unchanged". Seed one that
	// matches the manifest and the plan must agree.
	b := loadDemoManifest(t)
	api := newFakeAPI(t)
	api.routinesBySlug["demo-fetch-and-report"] = map[string]any{
		"id":   "pl_1",
		"slug": "demo-fetch-and-report",
		"name": "Fetch and report",
		"definition": map[string]any{
			"dsl_version": "1.0",
			"description": "Fetch a URL and post what came back. Runs with nothing configured.",
		},
	}

	plan, err := BuildPlan(context.Background(), NewClient(api), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	item := findPlanItem(plan, "routine", "demo-fetch-and-report")
	if item == nil {
		t.Fatalf("no routine item in the plan: %s", planKinds(plan))
	}
	if item.Action == ActionCreate {
		t.Errorf("the routine already exists remotely but the plan wants to create it — "+
			"the phase is still passing nil for the remote. Got: %s", item.Description)
	}
}

func TestRoutinePhase_StillCreatesWhenAbsent(t *testing.T) {
	// The other half: deferring to a lookup must not stop it creating a
	// routine that genuinely is not there.
	b := loadDemoManifest(t)
	plan, err := BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	item := findPlanItem(plan, "routine", "demo-fetch-and-report")
	if item == nil || item.Action != ActionCreate {
		t.Fatalf("want a create for an absent routine, got %+v", item)
	}
}

func TestComposioGrant_UnchangedWhenAlreadyBound(t *testing.T) {
	// Same class of bug in the code added for this feature: the grant was
	// appended unconditionally as a create, so every re-apply re-bound a
	// toolkit that was already bound and the plan never settled.
	b := loadDemoManifest(t)
	api := newFakeAPI(t)
	api.agentsBySlug["demo-riley"] = map[string]any{"id": "ag_1", "slug": "demo-riley"}
	api.composioBindings = []map[string]any{{"toolkit": "gmail", "mode": "read"}}

	plan, err := BuildPlan(context.Background(), NewClient(api), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if item := findPlanItem(plan, "composio_grant", "demo-riley:gmail"); item != nil &&
		item.Action == ActionCreate {
		t.Errorf("the grant already exists but the plan wants to create it again: %s", item.Description)
	}
}
