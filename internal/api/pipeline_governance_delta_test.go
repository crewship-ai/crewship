package api

import (
	"testing"
)

// The gate classified ABSOLUTE risk on every save, so a routine that
// declares credentials_required was risky forever: fixing a typo in its
// description demoted an already-approved routine back to `proposed`.
// And the person doing it was usually an OWNER — the same person who
// then clicks Approve. That is not a control, it is a ritual, and a
// ritual teaches people to click Approve without reading.
//
// Two rules replace it, both about whether there is anything NEW to
// review.

func TestGateDecision_UnchangedRiskOnAnApprovedRoutineStaysActive(t *testing.T) {
	got := gateDecision(gateInput{
		currentStatus: "active",
		priorReasons:  []string{"declares credentials", "declares http egress"},
		newReasons:    []string{"declares credentials", "declares http egress"},
	})
	if got.status != "active" {
		t.Fatalf("an approved routine whose risk did not change should stay active, got %q (%s)",
			got.status, got.why)
	}
}

func TestGateDecision_ReducedRiskStaysActive(t *testing.T) {
	// Removing the http step leaves a strict subset. There is strictly
	// less to review than was already approved.
	got := gateDecision(gateInput{
		currentStatus: "active",
		priorReasons:  []string{"declares credentials", "declares http egress"},
		newReasons:    []string{"declares credentials"},
	})
	if got.status != "active" {
		t.Fatalf("reducing risk should not require re-approval, got %q", got.status)
	}
}

func TestGateDecision_NewRiskGoesForReview(t *testing.T) {
	got := gateDecision(gateInput{
		currentStatus: "active",
		priorReasons:  []string{"declares credentials"},
		newReasons:    []string{"declares credentials", "declares http egress"},
	})
	if got.status != "proposed" {
		t.Fatalf("a NEW risk factor must go for review, got %q", got.status)
	}
}

func TestGateDecision_NewRiskOnAnApprovedRoutineStillGoesForReview(t *testing.T) {
	// The companion rule "an approver does not propose to themselves"
	// was rejected: Approve is gated on the same role tier as save, so
	// it would switch the gate off entirely rather than make it
	// proportionate. A newly added risk is still a prompt.
	got := gateDecision(gateInput{
		currentStatus: "active",
		priorReasons:  nil,
		newReasons:    []string{"declares http egress"},
	})
	if got.status != "proposed" {
		t.Fatalf("newly added egress must still be reviewed, got %q", got.status)
	}
}

func TestGateDecision_NewRoutineIsJudgedOnItsOwnMerits(t *testing.T) {
	// No current status: nothing was ever approved, so a subset check
	// has nothing to compare against and must not wave it through.
	got := gateDecision(gateInput{
		currentStatus: "",
		priorReasons:  nil,
		newReasons:    []string{"declares credentials"},
	})
	if got.status != "proposed" {
		t.Fatalf("a brand new risky routine must be reviewed, got %q", got.status)
	}
}

func TestGateDecision_SafeRoutineIsAlwaysActive(t *testing.T) {
	got := gateDecision(gateInput{currentStatus: "", newReasons: nil})
	if got.status != "active" {
		t.Fatalf("a routine with no risk factors is active, got %q", got.status)
	}
}

func TestGateDecision_ADisabledRoutineIsNotQuietlyReactivated(t *testing.T) {
	// `disabled` is an admin airbag. An unchanged-risk save must not be
	// the thing that turns a killed routine back on.
	got := gateDecision(gateInput{
		currentStatus: "disabled",
		priorReasons:  []string{"declares credentials"},
		newReasons:    []string{"declares credentials"},
	})
	if got.status == "active" {
		t.Fatalf("a disabled routine must not be reactivated by a save, got %q", got.status)
	}
}
