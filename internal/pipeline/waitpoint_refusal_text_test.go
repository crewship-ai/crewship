package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// #5348: a MANAGER deciding a held action was told "an agent cannot" first,
// the generic decider prefix, although the decider is a person. The error
// must lead with the held-action rule and still be ErrDeciderNotAllowed so
// the API keeps answering 403.
func TestDecide_HeldActionRefusalLeadsWithTheRule(t *testing.T) {
	_, _, wp, _, _, db := runHeldCrewship(t)
	var token string
	if err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	err := wp.Decide(context.Background(), "ws_test", token, true, WaitpointDecider{Kind: DeciderUser, ID: "u_manager"}, "")
	if !errors.Is(err, ErrDeciderNotAllowed) {
		t.Fatalf("err = %v, want ErrDeciderNotAllowed", err)
	}
	if !strings.HasPrefix(err.Error(), "waitpoint: a held routine action needs a workspace OWNER or ADMIN") || strings.Contains(err.Error(), "an agent cannot") {
		t.Fatalf("refusal text %q must lead with the OWNER/ADMIN rule and not blame an agent", err.Error())
	}
}

// #5348: deciding a waitpoint whose run was cancelled answered "already
// decided or expired", which hid the cause. ExplainClosedWaitpoint names
// the state, stays ErrAlreadyDecided, and says nothing about a token of
// another workspace.
func TestExplainClosedWaitpoint(t *testing.T) {
	_, _, wp, _, _, db := runHeldCrewship(t)
	var token, runID string
	if err := db.QueryRow(`SELECT token, pipeline_run_id FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&token, &runID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := wp.CancelWaitpointsForRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	decideErr := wp.Decide(ctx, "ws_test", token, true, WaitpointDecider{Kind: DeciderUser, ID: "u_admin"}, "")
	if !errors.Is(decideErr, ErrAlreadyDecided) {
		t.Fatalf("decide on a cancelled waitpoint = %v, want ErrAlreadyDecided", decideErr)
	}
	explained := wp.ExplainClosedWaitpoint(ctx, "ws_test", token, decideErr)
	if !errors.Is(explained, ErrAlreadyDecided) || !strings.Contains(explained.Error(), "cancelled") {
		t.Fatalf("explained = %v, want an ErrAlreadyDecided naming the cancellation", explained)
	}
	if other := wp.ExplainClosedWaitpoint(ctx, "ws_other", token, decideErr); other.Error() != ErrAlreadyDecided.Error() {
		t.Fatalf("another workspace learns %q about the token", other.Error())
	}
	if !errors.Is(wp.ExplainClosedWaitpoint(ctx, "ws_test", token, ErrDeciderNotAllowed), ErrDeciderNotAllowed) {
		t.Fatal("errors other than ErrAlreadyDecided must pass through unchanged")
	}
}
