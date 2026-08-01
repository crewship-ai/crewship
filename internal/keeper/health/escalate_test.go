package health

import "testing"

// An ESCALATE is not a refusal. The tier policy converts EVERY ALLOW into an
// ESCALATE at a HumanApproval tier (internal/keeper/tier.go), and L4 sets that
// flag — so a workspace whose credentials are all L4 sits at an ALLOW rate of
// exactly zero while the Keeper is working precisely as designed. Twenty
// requests routed to a human for approval is the feature, not the outage.
//
// Alarming on it re-fires every cooldown forever, and an alarm that cries wolf
// gets muted before the day it matters — which is the failure MinSamples exists
// to argue against, arrived at from the other direction.
//
// What the alarm is actually for is the #1624 shape: the judge answering
// unusably and the fail-closed path turning every request into a DENY. So the
// floor belongs under "the request got somewhere" — granted or escalated — not
// under ALLOW alone.
func TestAlarm_EscalationIsNotARefusal(t *testing.T) {
	s := Stats{WorkspaceID: "ws1", Samples: 20, Escalate: 20}
	if a, raised := s.Alarm(); raised {
		t.Fatalf("alarmed on an all-ESCALATE workspace (kind=%q) — every L4 credential escalates by design, so this fires forever on a healthy instance", a.Kind)
	}
}

// The regression this alarm exists to catch must still fire: a judge that denies
// everything is the #1624 outage, and nothing else in the product notices it.
func TestAlarm_BlanketDenyStillFires(t *testing.T) {
	s := Stats{WorkspaceID: "ws1", Samples: 20, Deny: 20}
	a, raised := s.Alarm()
	if !raised {
		t.Fatal("a workspace denying 20 of 20 requests did not alarm — this is the exact #1624 failure")
	}
	if a.Kind != AlarmAllowCollapse {
		t.Errorf("kind = %q, want %q", a.Kind, AlarmAllowCollapse)
	}
}

// A mixed workspace where most requests are refused but some get through is
// still the outage shape — the floor is a floor, not an equality check.
func TestAlarm_MostlyDenyFires(t *testing.T) {
	s := Stats{WorkspaceID: "ws1", Samples: 100, Deny: 99, Allow: 1}
	if _, raised := s.Alarm(); !raised {
		t.Error("99% DENY did not alarm")
	}
}

// A healthy mix must stay quiet.
func TestAlarm_HealthyMixIsQuiet(t *testing.T) {
	s := Stats{WorkspaceID: "ws1", Samples: 20, Allow: 12, Deny: 5, Escalate: 3}
	if a, raised := s.Alarm(); raised {
		t.Errorf("alarmed on a healthy mix: kind=%q", a.Kind)
	}
}
