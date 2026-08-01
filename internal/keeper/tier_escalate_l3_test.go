package keeper

import "testing"

// The jump from L3 to L4 is too steep for a local judge.
//
// L3 is "administrative access to real infrastructure (SSH, database admin,
// cloud account)" and the model grants it alone; L4 is the first tier a human
// always sees. On a 9B judge that means the largest single trust step in the
// product sits between two adjacent tiers, and an operator who wants a person to
// confirm SSH-to-production has only one move available: relabel the credential
// L4, which also drags in the four-eyes rule and the L4 intent minimum whether
// they wanted them or not.
//
// EscalateFrom is that missing dial. It raises the HumanApproval floor to a tier
// the operator names, and it can only ever TIGHTEN — the same one-directional
// rule the rest of this file is built on. Passing a level above L4, or below the
// tier being evaluated, changes nothing.
func TestTierPolicy_EscalateFromRaisesHumanApproval(t *testing.T) {
	// Default: L3 is decided by the model.
	if SecurityLevelL3.Tier().HumanApproval {
		t.Fatal("L3 requires human approval by default — that is the shape this dial exists to change")
	}

	p := SecurityLevelL3.TierWithEscalateFrom(SecurityLevelL3)
	if !p.HumanApproval {
		t.Error("EscalateFrom L3 did not put a human on L3")
	}
	if p.Label != SecurityLevelL3.Tier().Label {
		t.Errorf("label changed to %q — the tier is the same tier, only stricter", p.Label)
	}
}

// Tiers below the floor are untouched. An operator asking for a human on L3 has
// not asked for one on every npm read token.
func TestTierPolicy_EscalateFromLeavesLowerTiersAlone(t *testing.T) {
	for _, lvl := range []SecurityLevel{SecurityLevelL1, SecurityLevelL2} {
		p := lvl.TierWithEscalateFrom(SecurityLevelL3)
		if p.HumanApproval {
			t.Errorf("%v gained human approval from an L3 floor", lvl)
		}
		if lvl == SecurityLevelL1 && !p.AutoAllow {
			t.Error("L1 lost its auto-allow fast path")
		}
	}
}

// It can only tighten. A floor above L4 must not REMOVE the human L4 already
// requires — that would be the dial loosening a rule, which the tier vocabulary
// forbids outright.
func TestTierPolicy_EscalateFromNeverLoosens(t *testing.T) {
	p := SecurityLevelL4.TierWithEscalateFrom(SecurityLevel(0))
	if !p.HumanApproval {
		t.Error("L4 lost human approval to an unset floor")
	}
	if !p.SecondApprover {
		t.Error("L4 lost the four-eyes rule")
	}
	// And an unset floor is a no-op everywhere else.
	if SecurityLevelL3.TierWithEscalateFrom(SecurityLevel(0)).HumanApproval {
		t.Error("an unset floor escalated L3")
	}
}

// The dial does not drag the rest of L4's policy down with it. An operator who
// wants a person to confirm L3 has not asked for L4's four-eyes rule or L4's
// 35-character intent minimum — that bundling is exactly what relabelling the
// credential L4 does, and the reason this dial exists.
func TestTierPolicy_EscalateFromDoesNotImportTheWholeTier(t *testing.T) {
	p := SecurityLevelL3.TierWithEscalateFrom(SecurityLevelL3)
	l3 := SecurityLevelL3.Tier()
	if p.SecondApprover != l3.SecondApprover {
		t.Error("EscalateFrom forced the four-eyes rule onto L3")
	}
	if p.MinIntentChars != l3.MinIntentChars {
		t.Errorf("MinIntentChars = %d, want L3's %d", p.MinIntentChars, l3.MinIntentChars)
	}
	if p.MinRisk != l3.MinRisk {
		t.Errorf("MinRisk = %d, want L3's %d", p.MinRisk, l3.MinRisk)
	}
}
