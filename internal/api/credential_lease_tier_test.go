package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// The auto-lease gate used to answer "which tiers are self-service" with a
// literal `>= int(keeper.SecurityLevelL3)` while the rest of Keeper answered it
// from tierPolicies. Two encodings of one rule drift silently, and the drift
// direction here is a privilege question: a standing grant where a lease was
// intended, or an expiring key underneath a running agent.
//
// These tests exist to make that drift impossible to land. The first pins
// today's behaviour tier by tier; the second is the one that actually bites,
// because it compares the gate against the table for every level — including the
// out-of-band ones where a reintroduced literal and the table disagree.

// leaseInputAt builds an otherwise-eligible input at the given level, so the
// only thing under test is the tier.
func leaseInputAt(level int) leaseIssueInput {
	return leaseIssueInput{
		AgentID:       "agent-1",
		CredentialID:  "cred-1",
		SecurityLevel: level,
		TTLSeconds:    900,
	}
}

// TestLeaseEligible_TierSplit pins the shipped behaviour: L1/L2 stay standing
// grants, L3/L4 are auto-leased. If this changes, it is a product decision and
// the docs change with it — it must never change as a side effect of a refactor.
func TestLeaseEligible_TierSplit(t *testing.T) {
	cases := []struct {
		level     keeper.SecurityLevel
		wantLease bool
		why       string
	}{
		{keeper.SecurityLevelL1, false, "boot-delivered self-service tier; expiring it would break the agent's own run"},
		{keeper.SecurityLevelL2, false, "boot-delivered self-service tier; expiring it would break the agent's own run"},
		{keeper.SecurityLevelL3, true, "Keeper-mediated: access must decay after the decision that granted it"},
		{keeper.SecurityLevelL4, true, "human-escalated: access must decay after the decision that granted it"},
	}
	for _, tc := range cases {
		t.Run(tc.level.String(), func(t *testing.T) {
			if got := leaseInputAt(int(tc.level)).leaseEligible(); got != tc.wantLease {
				t.Errorf("leaseEligible at %s = %v, want %v — %s", tc.level, got, tc.wantLease, tc.why)
			}
		})
	}
}

// TestLeaseEligible_DerivesFromTierTable is the anti-drift test. It asserts the
// gate is the NEGATION of TierPolicy.SelfServiceDelivery at every level, not a
// threshold that merely happens to agree with it today.
//
// The out-of-band levels are what make it more than a tautology: a literal
// `level >= 3` calls level 0 self-service, while Tier() resolves an unknown
// level to L4 (fail-closed). Reintroduce the literal and this test goes red
// there, which is precisely the case a garbage security_level would exploit to
// buy a standing grant.
func TestLeaseEligible_DerivesFromTierTable(t *testing.T) {
	levels := append([]keeper.SecurityLevel{}, keeper.SecurityLevels()...)
	levels = append(levels, 0, -1, 5, 99)

	for _, l := range levels {
		want := !l.Tier().SelfServiceDelivery
		if got := leaseInputAt(int(l)).leaseEligible(); got != want {
			t.Errorf("level %d: leaseEligible = %v but the tier table says SelfServiceDelivery = %v; "+
				"the gate must read the table, not a threshold of its own",
				int(l), got, l.Tier().SelfServiceDelivery)
		}
	}
}

// The tier is only one of the gate's conditions. Pin the rest so a refactor of
// the tier half cannot quietly drop the opt-in check or start leasing a grant
// that does not exist.
func TestLeaseEligible_NonTierPreconditions(t *testing.T) {
	base := leaseInputAt(int(keeper.SecurityLevelL4))
	if !base.leaseEligible() {
		t.Fatal("baseline L4 input should be eligible")
	}

	off := base
	off.TTLSeconds = 0
	if off.leaseEligible() {
		t.Error("auto-lease is opt-in: TTL 0 must be a no-op even at L4")
	}

	noAgent := base
	noAgent.AgentID = ""
	if noAgent.leaseEligible() {
		t.Error("no agent to lease to, yet the gate opened")
	}

	noCred := base
	noCred.CredentialID = ""
	if noCred.leaseEligible() {
		t.Error("no credential to lease, yet the gate opened")
	}
}
