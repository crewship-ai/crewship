package keeper

import (
	"strings"
	"testing"
)

// The tier table is a security policy, so the properties worth pinning are the
// ones an attacker or a mistake would exploit: that a tier can only tighten a
// verdict, that an unknown level is treated as the worst case rather than the
// best, and that L4 cannot be granted by the model alone.

func TestTier_EveryDefinedLevelHasAPolicy(t *testing.T) {
	for _, l := range SecurityLevels() {
		if !l.Valid() {
			t.Errorf("%s is in SecurityLevels but has no policy", l)
		}
		p := l.Tier()
		if p.Level != l {
			t.Errorf("%s resolves to a policy for %s", l, p.Level)
		}
		if p.Label == "" || p.Blast == "" {
			t.Errorf("%s has no label or blast description — the console and the judge both render them", l)
		}
		if len(p.Checks) == 0 {
			t.Errorf("%s contributes no checks to the prompt", l)
		}
	}
}

// The single most important property in this file. A row with a level we do not
// recognise is a row whose blast radius we do not know, and the safe reading of
// that is the strictest tier — otherwise writing a garbage level would be the
// cheapest way to bypass every control here.
func TestTier_UnknownLevelIsTreatedAsCritical(t *testing.T) {
	for _, l := range []SecurityLevel{0, -1, 5, 99} {
		p := l.Tier()
		if p.Level != SecurityLevelL4 {
			t.Errorf("level %d resolved to %s, want the L4 policy", int(l), p.Level)
		}
		if !p.HumanApproval {
			t.Errorf("level %d does not require human approval", int(l))
		}
		if l.Valid() {
			t.Errorf("level %d reports Valid()", int(l))
		}
	}
}

// Scrutiny has to be monotone. A higher tier that asked for less would be worse
// than having no tiers at all, because the operator would believe the opposite.
func TestTier_ScrutinyIncreasesWithLevel(t *testing.T) {
	levels := SecurityLevels()
	for i := 1; i < len(levels); i++ {
		lo, hi := levels[i-1].Tier(), levels[i].Tier()
		if hi.MinIntentChars < lo.MinIntentChars {
			t.Errorf("%s demands a shorter intent (%d) than %s (%d)",
				hi.Level, hi.MinIntentChars, lo.Level, lo.MinIntentChars)
		}
		if hi.MinRisk < lo.MinRisk {
			t.Errorf("%s has a lower risk floor (%d) than %s (%d)",
				hi.Level, hi.MinRisk, lo.Level, lo.MinRisk)
		}
		if len(hi.Checks) < len(lo.Checks) {
			t.Errorf("%s asks fewer questions (%d) than %s (%d)",
				hi.Level, len(hi.Checks), lo.Level, len(lo.Checks))
		}
		if hi.AutoAllow && !lo.AutoAllow {
			t.Errorf("%s auto-allows but %s does not", hi.Level, lo.Level)
		}
	}
	// Only the lowest tier may skip the judge entirely.
	if !SecurityLevelL1.Tier().AutoAllow {
		t.Error("L1 lost its fast path — every low-value read would now cost a model call")
	}
	for _, l := range []SecurityLevel{SecurityLevelL2, SecurityLevelL3, SecurityLevelL4} {
		if l.Tier().AutoAllow {
			t.Errorf("%s auto-allows without reaching the judge", l)
		}
	}
}

// SelfServiceDelivery is the delivery half of the tier vocabulary, and it is the
// field the auto-lease gate reads. Pinning the exact split here is what stops a
// surface from re-deriving "which tiers are self-service" with a literal of its
// own: if this table changes, the gate changes with it, and if it does not, the
// companion test in internal/api fails.
func TestTier_SelfServiceDeliveryIsTheLowerTwoTiers(t *testing.T) {
	want := map[SecurityLevel]bool{
		SecurityLevelL1: true,
		SecurityLevelL2: true,
		SecurityLevelL3: false,
		SecurityLevelL4: false,
	}
	for _, l := range SecurityLevels() {
		if got := l.Tier().SelfServiceDelivery; got != want[l] {
			t.Errorf("%s SelfServiceDelivery = %v, want %v", l, got, want[l])
		}
	}
}

// Delivery has to be monotone for the same reason scrutiny is: a higher tier
// that handed the agent the raw value for the whole run, while a lower one
// leased it, would invert the meaning of the ladder.
func TestTier_SelfServiceDeliveryStopsOnceAndDoesNotComeBack(t *testing.T) {
	levels := SecurityLevels()
	for i := 1; i < len(levels); i++ {
		lo, hi := levels[i-1].Tier(), levels[i].Tier()
		if hi.SelfServiceDelivery && !lo.SelfServiceDelivery {
			t.Errorf("%s is self-service but %s is not", hi.Level, lo.Level)
		}
	}
	// A tier that both skips the judge and would be leased is incoherent: the
	// lease exists to bound a Keeper-mediated grant, and there is no decision to
	// bound if no judge ever ran.
	for _, l := range levels {
		p := l.Tier()
		if p.AutoAllow && !p.SelfServiceDelivery {
			t.Errorf("%s auto-allows without a judge yet is not self-service delivered", l)
		}
	}
}

// The fail-closed default has to reach this field too. An unknown level resolves
// to L4, so a corrupt row must be treated as Keeper-mediated (leased), never as
// the self-service tier that hands the agent a standing secret.
func TestTier_UnknownLevelIsNotSelfService(t *testing.T) {
	for _, l := range []SecurityLevel{0, -1, 5, 99} {
		if l.Tier().SelfServiceDelivery {
			t.Errorf("level %d resolved to a self-service policy — a garbage level would buy a standing grant", int(l))
		}
	}
}

// The reason an operator marks a credential L4: the model may vouch for a
// request but must not grant it.
func TestApplyTierFloor_CriticalCannotBeGrantedByTheModel(t *testing.T) {
	decision, risk, note := ApplyTierFloor(SecurityLevelL4, string(DecisionAllow), 3)

	if decision != string(DecisionEscalate) {
		t.Errorf("decision = %s, want ESCALATE", decision)
	}
	if risk < SecurityLevelL4.Tier().MinRisk {
		t.Errorf("risk = %d, want at least the L4 floor %d", risk, SecurityLevelL4.Tier().MinRisk)
	}
	if note == "" {
		t.Error("no note explaining why the ALLOW did not stand")
	}
	// The human reading the escalation has to learn WHY from the reason, not from
	// the tier number alone.
	if !strings.Contains(note, "human") {
		t.Errorf("note %q does not say a human has to approve", note)
	}
}

// L3 is high, not critical: the judge can still grant it. Conflating the two
// would make L4 meaningless and flood the inbox.
func TestApplyTierFloor_HighTierStillGrantable(t *testing.T) {
	decision, risk, note := ApplyTierFloor(SecurityLevelL3, string(DecisionAllow), 2)
	if decision != string(DecisionAllow) {
		t.Errorf("decision = %s, want ALLOW to stand at L3", decision)
	}
	if note != "" {
		t.Errorf("note = %q, want none when nothing was changed", note)
	}
	// But the risk floor still applies, because DENY-notify is a risk comparison
	// and a high-tier decision scored 2 would never reach the inbox.
	if risk != SecurityLevelL3.Tier().MinRisk {
		t.Errorf("risk = %d, want the L3 floor %d", risk, SecurityLevelL3.Tier().MinRisk)
	}
	// The two lower tiers leave the judge's number alone: their decisions are not
	// ones a human is paged about, and rewriting the score would corrupt the audit
	// record of what the judge actually thought.
	for _, l := range []SecurityLevel{SecurityLevelL1, SecurityLevelL2} {
		if _, r, _ := ApplyTierFloor(l, string(DecisionAllow), 1); r != 1 {
			t.Errorf("%s rewrote a risk score of 1 to %d", l, r)
		}
	}
}

// Only ever tighten. A tier that could turn a DENY into anything else would be a
// privilege escalation with extra steps.
func TestApplyTierFloor_NeverLoosens(t *testing.T) {
	for _, l := range SecurityLevels() {
		for _, in := range []string{string(DecisionAllow), string(DecisionDeny), string(DecisionEscalate)} {
			out, risk, _ := ApplyTierFloor(l, in, 9)
			if in == string(DecisionDeny) && out != string(DecisionDeny) {
				t.Errorf("%s turned a DENY into %s", l, out)
			}
			if in == string(DecisionEscalate) && out == string(DecisionAllow) {
				t.Errorf("%s turned an ESCALATE into an ALLOW", l)
			}
			if risk < 9 {
				t.Errorf("%s lowered the judge's risk score from 9 to %d", l, risk)
			}
		}
	}
}

// A malformed level must not be the cheap path to an ALLOW.
func TestApplyTierFloor_UnknownLevelEscalates(t *testing.T) {
	if decision, _, _ := ApplyTierFloor(0, string(DecisionAllow), 1); decision != string(DecisionEscalate) {
		t.Errorf("level 0 ALLOW = %s, want ESCALATE", decision)
	}
	if decision, _, _ := ApplyTierFloor(7, string(DecisionAllow), 1); decision != string(DecisionEscalate) {
		t.Errorf("level 7 ALLOW = %s, want ESCALATE", decision)
	}
}

func TestSecurityLevel_Rendering(t *testing.T) {
	if got := SecurityLevelL3.String(); got != "L3" {
		t.Errorf("String() = %q, want L3", got)
	}
	if got := SecurityLevelL4.Label(); !strings.Contains(got, "critical") {
		t.Errorf("Label() = %q, want it to name the tier", got)
	}
}
