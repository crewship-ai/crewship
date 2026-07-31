package keeper

import "fmt"

// Credential tiers, and what each one actually changes.
//
// SecurityLevel L1–L4 has been on the credentials table since v2 and in the
// prompt since the first gatekeeper. What it did not have was consequences: L2,
// L3 and L4 reached the judge with an identical prompt and an identical decision
// space, so the only difference between "npm read token" and "production
// database admin" was one line of text the model was free to ignore. The L4 doc
// comment said "human approval, future work" for three milestones.
//
// This file is that work. A tier is a policy, not a label:
//
//   - L1 keeps the auto-allow fast path (no model call, no cost) — that is the
//     point of having a lowest tier.
//   - L2 and up always reach the judge.
//   - L3 and up demand an intent that says something. A four-word intent for a
//     database admin credential is not a justification, and rejecting it before
//     the model call is both cheaper and a clearer message than letting the
//     judge guess.
//   - L4 can never be granted by the model alone. An ALLOW is upgraded to
//     ESCALATE, so a human confirms every production-admin credential read. This
//     is the whole reason an operator marks something L4.
//   - L1 and L2 are delivered to the agent for the whole run; L3 and L4 are
//     mediated per read, which is why only their grants auto-lease. See
//     SelfServiceDelivery.
//
// The floors are deliberately one-directional: a tier can only make a verdict
// stricter, never looser. A judge that wants to DENY an L1 read still denies it.
type TierPolicy struct {
	// Level is the tier this policy belongs to.
	Level SecurityLevel
	// Label is the operator-facing name — what the console, CLI and inbox call
	// this tier. Kept here so the four surfaces cannot drift into calling L3
	// three different things.
	Label string
	// Blast is the one-line description of what a credential at this tier can
	// do. Rendered to the judge and to the human reading a finding: "critical"
	// on its own does not tell either of them what is at stake.
	Blast string

	// AutoAllow permits the no-model fast path (L1 only).
	AutoAllow bool
	// SelfServiceDelivery marks the tiers whose value is handed to the agent for
	// the whole run — boot env vars, /secrets files, the sidecar credstore — as
	// opposed to being mediated by Keeper per read.
	//
	// It is the delivery half of the tier vocabulary, and it is what the
	// credential auto-lease gate reads. A lease bounds a Keeper decision: it
	// makes access decay after the ALLOW that granted it. There is no decision to
	// bound on a self-service tier, and expiring one of those mid-run would not
	// contain an attacker — it would break the agent's own LLM calls with an
	// invalid key. So L1/L2 stay standing grants and L3/L4 are leased.
	//
	// This lived as a comment on governance.AutoLeaseSeconds and as a literal
	// `>= L3` in internal/api until #1557. It is a field for the same reason
	// Label is: a rule two surfaces spell out independently is a rule that will
	// eventually be spelled two different ways, and here the drift would silently
	// grant standing access where a lease was intended.
	SelfServiceDelivery bool
	// MinIntentChars is the shortest intent this tier accepts. Requests below it
	// are denied before the model call, with a reason that says what to add.
	MinIntentChars int
	// HumanApproval upgrades an ALLOW to ESCALATE: the model may vouch for the
	// request but not grant it.
	HumanApproval bool
	// SecondApprover forces the four-eyes rule on resolution regardless of the
	// workspace toggle — the person whose agent asked cannot be the person who
	// approves.
	SecondApprover bool
	// MinRisk is the floor under the judge's risk score, and 0 means "leave the
	// judge's number alone".
	//
	// It exists because the DENY-notify threshold is a risk comparison: a judge
	// that returns risk 2 for a production-admin DENY keeps that DENY out of the
	// inbox, which is the one place it needed to appear. It is deliberately 0 on
	// the two lower tiers — their decisions are not ones a human needs paged
	// about, and rewriting the model's score there would corrupt the audit record
	// (what the judge actually thought) to no end.
	MinRisk int
	// Checks are the tier-specific questions added to the judge's prompt. Higher
	// tiers get more, and they get more specific.
	Checks []string
}

// tierPolicies is the table. Unknown levels resolve to L4 — see Tier.
var tierPolicies = map[SecurityLevel]TierPolicy{
	SecurityLevelL1: {
		Level: SecurityLevelL1, Label: "L1 · low",
		Blast:               "read-only or low-value access (npm read token, public API key)",
		AutoAllow:           true,
		SelfServiceDelivery: true,
		MinIntentChars:      10,
		Checks: []string{
			"Does the stated intent describe actual work, rather than restating the credential's name?",
		},
	},
	SecurityLevelL2: {
		Level: SecurityLevelL2, Label: "L2 · medium",
		Blast:               "write access to a non-production system (GitHub write, staging database)",
		SelfServiceDelivery: true,
		MinIntentChars:      15,
		Checks: []string{
			"Does the stated intent describe actual work, rather than restating the credential's name?",
			"Is a WRITE-capable credential actually needed, or would a read-only one do?",
		},
	},
	SecurityLevelL3: {
		Level: SecurityLevelL3, Label: "L3 · high",
		Blast:          "administrative access to real infrastructure (SSH, database admin, cloud account)",
		MinIntentChars: 25,
		MinRisk:        4,
		Checks: []string{
			"Does the conversation history independently corroborate that this work is underway? Absence of corroboration is grounds to ESCALATE, not to ALLOW.",
			"Is this credential the narrowest one that can do the stated job?",
			"Would the stated task still make sense if the credential were scoped down or time-boxed?",
			"Does the request arrive at a plausible point in the agent's work, or out of nowhere?",
		},
	},
	SecurityLevelL4: {
		Level: SecurityLevelL4, Label: "L4 · critical",
		Blast:          "production administration, payments, or customer data at scale",
		MinIntentChars: 35,
		HumanApproval:  true,
		SecondApprover: true,
		// 6 is the shipped DENY-notify default, so a critical decision clears the
		// bar an unconfigured workspace is already using.
		MinRisk: 6,
		Checks: []string{
			"Does the conversation history independently corroborate that this work is underway? Absence of corroboration is grounds to ESCALATE, not to ALLOW.",
			"Is this credential the narrowest one that can do the stated job?",
			"Is there any reading of the intent under which this credential could be used to exfiltrate, destroy, or bill? If so, say so in the reason.",
			"Is the requesting agent one that would plausibly need production administration at all?",
			"Would you be comfortable defending this grant in an incident review? If not, ESCALATE.",
		},
	},
}

// Valid reports whether l is one of the four defined tiers.
func (l SecurityLevel) Valid() bool {
	_, ok := tierPolicies[l]
	return ok
}

// Tier returns the policy for l.
//
// An out-of-range level resolves to the L4 policy, not to L1. A row with a
// corrupt or future level is a row we know nothing about, and the safe reading of
// "unknown blast radius" is the strictest tier — the same fail-closed default the
// rest of Keeper takes. Getting this backwards would make a malformed level the
// cheapest way to bypass every control in this file.
func (l SecurityLevel) Tier() TierPolicy {
	if p, ok := tierPolicies[l]; ok {
		return p
	}
	return tierPolicies[SecurityLevelL4]
}

// Label is the operator-facing tier name, e.g. "L3 · high".
func (l SecurityLevel) Label() string { return l.Tier().Label }

// String renders the bare tier, e.g. "L3".
func (l SecurityLevel) String() string { return fmt.Sprintf("L%d", int(l)) }

// SecurityLevels is every defined tier, ascending — for a picker or a --help
// line that must not drift from the table above.
func SecurityLevels() []SecurityLevel {
	return []SecurityLevel{SecurityLevelL1, SecurityLevelL2, SecurityLevelL3, SecurityLevelL4}
}

// RefusalRisk is the risk score attached to a refusal this package makes on its
// own (a too-thin intent), as opposed to one the judge returned.
//
// It is not a finding about the request — nobody inspected it — but the field is
// a 1..10 scale with no "not assessed" value, so the honest answer is the tier's
// floor where it has one and a low-but-nonzero number where it does not.
func (p TierPolicy) RefusalRisk() int {
	if p.MinRisk > 0 {
		return p.MinRisk
	}
	return 3
}

// ApplyTierFloor tightens a judge verdict to what the tier permits, returning the
// decision, the risk score, and a suffix to append to the reason (empty when
// nothing changed).
//
// Only ever tightens. ALLOW → ESCALATE at a human-approval tier; risk is raised
// to the tier floor but never lowered. A DENY is returned untouched: the judge
// refusing is already the strictest outcome, and re-labelling it would lose the
// reason the human needs.
func ApplyTierFloor(level SecurityLevel, decision string, risk int) (string, int, string) {
	p := level.Tier()
	note := ""

	if risk < p.MinRisk {
		risk = p.MinRisk
	}
	if decision == string(DecisionAllow) && p.HumanApproval {
		decision = string(DecisionEscalate)
		note = fmt.Sprintf(" — %s credential: a human must approve this read, so the judge's approval alone does not grant it",
			p.Label)
	}
	return decision, risk, note
}
