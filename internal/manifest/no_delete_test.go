package manifest

import (
	"errors"
	"testing"
)

// --no-delete has to be enforced by Apply, not only by the CLI that
// prints the plan.
//
// The CLI checks the plan it built itself, then calls Apply — which
// builds its OWN plan from a fresh read of the server. Between those two
// reads a resource can disappear from the workspace, and the second plan
// grows a delete the first one never had. Apply's only destructive gate
// is `HasDestructive() && !Yes`, and the documented CI invocation passes
// --yes, so that delete would execute under the flag whose entire
// purpose is to make it impossible.
//
// Narrow race, but the flag's claim is "a guarantee CI can check", and a
// guarantee with a window is a different product than the one the docs
// describe. The guard belongs next to HasDestructive, where the plan
// that is about to run is the plan being judged.
func TestApplyOptions_NoDeleteRefusesEvenWithYes(t *testing.T) {
	opts := Options{NoDelete: true, Yes: true}

	plan := &Plan{Items: []PlanItem{
		{Action: ActionCreate, Kind: "crew", Description: "uctarna"},
		{Action: ActionDelete, Kind: "agent", Description: "uctarna/sberac"},
	}}

	err := plan.checkNoDelete(opts)
	if err == nil {
		t.Fatal("--no-delete let a destructive plan through under --yes")
	}
	if !errors.Is(err, ErrDeletesRefused) {
		t.Errorf("err = %v, want ErrDeletesRefused so callers can branch on it", err)
	}
	// The refusal has to name what it would have destroyed, or the
	// operator's next move is to re-run without the flag to find out.
	if got := err.Error(); !contains(got, "uctarna/sberac") {
		t.Errorf("refusal does not name the resource: %v", got)
	}
}

func TestApplyOptions_NoDeleteAllowsACleanPlan(t *testing.T) {
	plan := &Plan{Items: []PlanItem{
		{Action: ActionCreate, Kind: "crew", Description: "uctarna"},
		{Action: ActionUnchanged, Kind: "agent", Description: "uctarna/kontrolor"},
	}}
	if err := plan.checkNoDelete(Options{NoDelete: true, Yes: true}); err != nil {
		t.Errorf("blocked a plan with no deletes: %v", err)
	}
}

// Without the flag, nothing changes: deletes still go through the
// existing confirmation gate rather than this one.
func TestApplyOptions_NoDeleteOffIsInert(t *testing.T) {
	plan := &Plan{Items: []PlanItem{
		{Action: ActionDelete, Kind: "agent", Description: "uctarna/sberac"},
	}}
	if err := plan.checkNoDelete(Options{Yes: true}); err != nil {
		t.Errorf("guard fired without --no-delete: %v", err)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
