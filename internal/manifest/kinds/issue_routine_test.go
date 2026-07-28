package kinds

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// An issue can be bound to a routine — missions.routine_id has existed since
// migration v84, and both the create and update handlers accept `routine_id`.
// The manifest could not say so, which is why a demo workspace could describe
// an issue and a routine but never the link between them: the one thing the
// demo is meant to show.
//
// The manifest carries a SLUG, like every other cross-resource reference here
// (crew_slug, project_slug, assignee_slug). Ids are server-minted and would
// make a manifest unportable between instances.

func TestIssue_Plan_CreateBindsRoutineBySlug(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.RoutineSlug = "nightly-probe"
	client := newIssueFake()
	issueSeedFakeFull(client)
	client.routines["nightly-probe"] = issueRoutineStub{ID: "pl_nightly", Slug: "nightly-probe"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	call := client.findCall("POST", "/api/v1/crews/crew_eng/issues")
	if call == nil {
		t.Fatal("expected the create call to be recorded")
	}
	body := call.Body.(map[string]any)
	if got, _ := body["routine_id"].(string); got != "pl_nightly" {
		t.Errorf("routine_id = %q, want the resolved id pl_nightly", got)
	}
	// The server only knows ids; sending the slug too would be a field it
	// silently ignores, which is how a manifest starts lying about what it set.
	if _, has := body["routine_slug"]; has {
		t.Error("body must not carry routine_slug")
	}
}

func TestIssue_Plan_OmitsRoutineWhenUnset(t *testing.T) {
	// An issue with no routine is the common case. Sending routine_id: ""
	// would ask the server to bind to nothing, which it treats as clearing —
	// harmless on create, wrong on update.
	doc := issueSampleDoc()
	client := newIssueFake()
	issueSeedFakeFull(client)

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body := client.findCall("POST", "/api/v1/crews/crew_eng/issues").Body.(map[string]any)
	if _, has := body["routine_id"]; has {
		t.Errorf("routine_id must be absent when no routine is declared, got %v", body["routine_id"])
	}
}

func TestIssue_Plan_UnknownRoutineSlugFails(t *testing.T) {
	// Deferred to Exec, like the crew: the routine may be created by an
	// earlier item of this same apply, so it cannot be resolved while the
	// plan is built. One that never appears still fails, and still names
	// itself — a dangling reference must not pass silently.
	doc := issueSampleDoc()
	doc.Spec.RoutineSlug = "does-not-exist"
	client := newIssueFake()
	issueSeedFakeFull(client)

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan should defer, not fail: %v", err)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil {
		t.Fatal("expected Exec to fail on an unknown routine slug")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the offending slug, got: %v", err)
	}
}

func TestIssue_Plan_UpdateBindsARoutineThatDrifted(t *testing.T) {
	// The issue exists remotely with no routine; the manifest now declares
	// one. That is a drift the plan must repair, not ignore.
	doc := issueSampleDoc()
	doc.Spec.RoutineSlug = "nightly-probe"
	client := newIssueFake()
	issueSeedFakeFull(client)
	client.routines["nightly-probe"] = issueRoutineStub{ID: "pl_nightly", Slug: "nightly-probe"}
	remote := IssueRemote{
		ID:         "msn_1",
		CrewID:     "crew_eng",
		CrewSlug:   "engineering",
		Identifier: strPtr("ENG-1"),
		Title:      doc.resolvedTitle(),
		Priority:   "medium",
		ProjectID:  strPtr("proj_np"),
		AssigneeID: strPtr("agt_viktor"),
		Labels:     []issueRemoteLabel{{ID: "lbl_mon", Name: "monitoring"}, {ID: "lbl_net", Name: "network"}},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) == 0 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want an update item, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := client.findCall("PATCH", "/api/v1/crews/crew_eng/issues/ENG-1")
	if call == nil {
		t.Fatal("expected a PATCH to be recorded")
	}
	if got, _ := call.Body.(map[string]any)["routine_id"].(string); got != "pl_nightly" {
		t.Errorf("patch routine_id = %q, want pl_nightly", got)
	}
}

func TestIssue_Plan_AlreadyBoundRoutineIsNotRepatched(t *testing.T) {
	// Re-applying an unchanged binding must not put routine_id in the patch.
	// Asserted on the patch body rather than on "no update at all", because
	// the shared fixture drifts on other fields — this test is about the
	// binding, and a patch that re-sends a value it already matches is how
	// `apply` starts reporting churn that isn't there.
	doc := issueSampleDoc()
	doc.Spec.RoutineSlug = "nightly-probe"
	client := newIssueFake()
	issueSeedFakeFull(client)
	client.routines["nightly-probe"] = issueRoutineStub{ID: "pl_nightly", Slug: "nightly-probe"}
	remote := IssueRemote{
		ID:         "msn_1",
		CrewID:     "crew_eng",
		CrewSlug:   "engineering",
		Identifier: strPtr("ENG-1"),
		Title:      doc.resolvedTitle(),
		Priority:   "medium",
		ProjectID:  strPtr("proj_np"),
		AssigneeID: strPtr("agt_viktor"),
		RoutineID:  strPtr("pl_nightly"),
		Labels:     []issueRemoteLabel{{ID: "lbl_mon", Name: "monitoring"}, {ID: "lbl_net", Name: "network"}},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action == internalapi.ActionUnchanged {
		return // nothing to patch at all; the invariant holds trivially
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := client.findCall("PATCH", "/api/v1/crews/crew_eng/issues/ENG-1")
	if call == nil {
		t.Fatal("expected a PATCH to be recorded")
	}
	if _, has := call.Body.(map[string]any)["routine_id"]; has {
		t.Error("routine_id was re-sent even though it already matched")
	}
}

func TestIssue_Validate_RejectsUnknownRoutineSlug(t *testing.T) {
	// Caught at validate time when the bundle declares its own routines, so a
	// typo in a self-contained manifest fails before touching the network.
	doc := issueSampleDoc()
	doc.Spec.RoutineSlug = "typo-here"
	ctx := issueCtxFull()
	ctx.DeclaredRoutines = []internalapi.SlugLookup{{Slug: "nightly-probe"}}

	err := doc.Validate(ctx)
	if err == nil {
		t.Fatal("expected validation to reject an undeclared routine slug")
	}
	if !strings.Contains(err.Error(), "typo-here") {
		t.Errorf("error should name the slug, got: %v", err)
	}
}
