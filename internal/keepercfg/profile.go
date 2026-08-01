package keepercfg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/evidence"
)

// The judge profile: which capabilities the credential-access judge is allowed
// to use, as seven independent toggles plus three presets that set them.
//
// Why toggles and not constants. Every capability the Keeper 1.0 work adds —
// the computed evidence block, the deterministic hard gate, few-shot precedent,
// self-consistency sampling — makes the prompt bigger or the decision more
// expensive. A small model drowned in context decides WORSE, not better, and
// that is a hypothesis rather than a fact: measurement (PRD P4) has not run yet.
// Hard-coding either direction would bake today's guess into a security control
// an operator cannot re-aim at the model they actually have. The defaults below
// are therefore provisional by design, and the whole point is that they move.
//
// Why tri-state. `inherit` means "follow the profile", never "off". An operator
// switching one capability off must not silently pin the other six to whatever
// they happened to be that afternoon — otherwise the first person to turn
// precedent off freezes their instance out of every later default change.
//
// The layering, per toggle, lowest to highest precedence:
//
//	built-in profile  ←  selected preset  ←  per-toggle override
//	  SourceDefault       SourceProfile        SourceInstance
//
// This is the aux store's provenance discipline (aux.go) with one extra layer,
// because here a preset is a real decider: "the standard profile turned
// precedent on" and "somebody turned precedent on by hand" are different facts
// and a console that renders them alike teaches the operator nothing.

// ProfileName is a preset bundle of toggles, named for the class of model it
// suits. Sizes are guidance from the PRD's measurement on a 9B Q4 judge, not
// enforcement — nothing inspects the model to pick one.
type ProfileName string

const (
	// ProfileLean: the evidence block and the hard gate only. For ~3–9B models
	// and short contexts, where every extra section costs accuracy.
	ProfileLean ProfileName = "lean"
	// ProfileStandard: lean plus few-shot precedent. For 9–14B with num_ctx ≥ 8k.
	ProfileStandard ProfileName = "standard"
	// ProfileThorough: everything, including 3-sample self-consistency on L3/L4.
	// For hosted judges, where a third of a second per extra sample is affordable.
	ProfileThorough ProfileName = "thorough"
)

// DefaultProfile is what an instance nobody has configured runs. Lean rather
// than standard because the product's stated target is a judge that fits on a
// laptop: the conservative default is the one that keeps the prompt small, and
// an operator with a bigger model can say so in one command.
const DefaultProfile = ProfileLean

// Profiles is the display order — cheapest first, so the list reads as a dial.
var Profiles = []ProfileName{ProfileLean, ProfileStandard, ProfileThorough}

// KnownProfile reports whether n names a preset.
func KnownProfile(n ProfileName) bool {
	for _, p := range Profiles {
		if p == n {
			return true
		}
	}
	return false
}

// EvidenceFacts is the vocabulary of computed facts the judge prompt may carry
// (PRD P1). Each is one indexed query over a table that already exists; the
// order here is the order they are rendered in, cheapest and most decisive
// first, because a truncated prompt loses the tail.
//
// A name that validates here but means nothing downstream would silently drop
// evidence from a security decision, and the failure mode of a missing fact is a
// confident ALLOW — so unknown names are refused rather than ignored.
//
// Which makes the list itself the risk. It is DERIVED from the collector, never
// restated: the collector is the half that can actually produce a fact, so it
// owns the vocabulary. Two hand-maintained copies drifted within a day of both
// being written — `prior_grants_same_pair` here against
// `prior_requests_same_pair` there, plus two names nothing computed — and
// nothing caught it, because the coupling was a comment asking a sibling package
// nicely. See evidence.FactKeys and
// TestEvidenceFacts_ProfileAndCollectorShareOneVocabulary.
//
// `crew scope` is deliberately absent: it does not exist in the schema, and an
// earlier draft of the PRD measured with it before that was caught.
var EvidenceFacts = evidence.FactKeys()

// KnownEvidenceFact reports whether f names a fact the collector can produce.
func KnownEvidenceFact(f string) bool {
	for _, known := range EvidenceFacts {
		if known == f {
			return true
		}
	}
	return false
}

// SourceProfile is the layer between the built-in and a per-toggle override: a
// preset the operator selected. Declared here rather than next to the other
// sources because only the profile block can produce it.
const SourceProfile Source = "profile"

// Bounds on the numeric toggles.
//
// MaxPrecedentExamples: precedent is few-shot priming, not retrieval — past ten
// examples the prompt is mostly history and the current request is what gets
// truncated. MaxConsistencySamples: at ~3.4s a verdict, nine samples already
// exceeds the 20s default decision budget, and P5 requires the budget check to
// happen BEFORE sampling rather than as a timeout.
//
// MinPromptBudgetTokens is the floor at which the incompressible sections (the
// watch policy, the tier, the facts, the request itself) still fit; below it the
// budget would force the truncation of a security instruction, which is the
// exact silent degradation P7 exists to prevent. MaxPromptBudgetTokens is a
// sanity ceiling on a hand-typed number, not a model limit.
const (
	MaxPrecedentExamples   = 10
	MaxConsistencySamples  = 9
	MinPromptBudgetTokens  = 512
	MaxPromptBudgetTokens  = 131072
	maxEvidenceFactsLength = 512
)

// profileValues is one fully-decided set of toggles — a preset, or the built-in.
// No provenance: this is the input to layering, not its output.
type profileValues struct {
	evidence bool
	hardGate bool
	// escalateFrom sets the HumanApproval floor to this credential tier (1-4),
	// or 5 for "never" — full autonomy, no tier escalated on the model's behalf.
	// 0 leaves the tier table alone, which is the default everywhere: this is a
	// deliberate tightening an operator asks for, never something a preset
	// imposes on a workspace that did not choose it.
	escalateFrom       int64
	precedent          bool
	precedentN         int64
	consistencySamples int64
	// promptBudgetTokens 0 means NO CAP — the model server decides what to drop,
	// which is the pre-P7 behaviour.
	//
	// An earlier draft said 0 would be "derived from the judge's num_ctx at
	// prompt-build time". Nothing derived it, and deriving it honestly would mean
	// asking the model server for its context length on the credential hot path.
	// So the presets carry real numbers instead: the ceiling still belongs to the
	// model, but an operator picks the profile that matches the model they have.
	promptBudgetTokens int64
	// evidenceFacts nil means every fact in EvidenceFacts. No preset narrows the
	// selection today — narrowing is an operator's answer to a specific model
	// getting worse with a specific fact, which is measurement (P4), not policy.
	evidenceFacts []string
}

// profilePresets is the PRD P0 table. Deliberately data rather than code: the
// defaults are provisional until P4 measures them, and a table is what makes
// "we changed the standard profile" a one-line diff.
var profilePresets = map[ProfileName]profileValues{
	// The budgets are the model classes each profile names, minus the 256-token
	// verdict and a margin: lean targets a 4096-token judge (the reference
	// deployment), standard an 8k one. Thorough is for hosted judges whose
	// context is large enough that a cap would only ever fire on a runaway
	// conversation the model server can handle itself.
	ProfileLean:     {evidence: true, hardGate: true, precedent: false, precedentN: 3, consistencySamples: 1, promptBudgetTokens: 3500},
	ProfileStandard: {evidence: true, hardGate: true, precedent: true, precedentN: 3, consistencySamples: 1, promptBudgetTokens: 7000},
	ProfileThorough: {evidence: true, hardGate: true, precedent: true, precedentN: 3, consistencySamples: 3, promptBudgetTokens: 0},
}

// profileSettings is the stored half: which preset, plus per-toggle overrides.
// A nil pointer or an empty string is "inherit", exactly as in the judge row
// above it.
type profileSettings struct {
	profile            string
	evidence           *bool
	evidenceFacts      string
	hardGate           *bool
	escalateFrom       *int64
	precedent          *bool
	precedentN         *int64
	consistencySamples *int64
	promptBudgetTokens *int64
}

func (p profileSettings) empty() bool {
	return p.profile == "" && p.evidence == nil && p.evidenceFacts == "" && p.hardGate == nil &&
		p.precedent == nil && p.precedentN == nil && p.consistencySamples == nil &&
		p.promptBudgetTokens == nil
}

// EffectiveProfile is the resolved judge profile: every toggle in force plus the
// layer that decided it.
type EffectiveProfile struct {
	// Name is the preset in force. Source is `instance` when an operator picked
	// it and `default` when it is the built-in — the profile itself has no third
	// layer to inherit from.
	Name Field[string] `json:"name"`
	// Evidence gates the computed-facts block (P1).
	Evidence Field[bool] `json:"evidence"`
	// EvidenceFacts is which facts that block carries. Always the full resolved
	// list, never a "" meaning all — a caller that has to re-derive the default
	// is a caller that will get it wrong.
	EvidenceFacts Field[[]string] `json:"evidence_facts"`
	// HardGate is the deterministic refusal of an unbound credential, taken
	// before the model is called at all (P1).
	HardGate Field[bool] `json:"hard_gate"`
	// EscalateFrom is the tier from which a judge ALLOW becomes an ESCALATE, so a
	// human confirms it (P8). 0 = the tier table decides.
	//
	// It exists because the step from L3 (the model grants administrative access
	// to real infrastructure alone) to L4 (a person always confirms) was the
	// largest trust jump in the product with no dial between. The alternative was
	// relabelling the credential L4, which also imposes the four-eyes rule and
	// L4's intent minimum whether the operator wanted them or not.
	EscalateFrom Field[int64] `json:"escalate_from"`
	// Precedent is the few-shot block of past human-resolved decisions (P3).
	Precedent  Field[bool]  `json:"precedent"`
	PrecedentN Field[int64] `json:"precedent_n"`
	// ConsistencySamples is how many verdicts to take on L3/L4 before a majority
	// vote (P5). 1 means single-sample, i.e. self-consistency off.
	ConsistencySamples Field[int64] `json:"consistency_samples"`
	// PromptBudgetTokens caps the assembled prompt (P7). 0 means no cap; see
	// the judge's context window.
	PromptBudgetTokens Field[int64] `json:"prompt_budget_tokens"`

	// Overridden is true when a preset is selected or any toggle is set — what a
	// "Reset to the built-in profile" control needs before offering itself.
	Overridden bool `json:"overridden"`
}

// Stamp is the audit form: the profile name followed by every toggle in force,
// in a fixed order. It goes on the decision record because the NAME alone is
// not enough to compare two decisions — a `standard` with precedent switched off
// by hand is a different judge from `standard`, and an evaluation that cannot
// tell them apart is measuring noise.
//
// Kept short and stable rather than JSON: it is stored per keeper_requests row
// and read by eyes as often as by code.
func (p EffectiveProfile) Stamp() string {
	facts := "all"
	if len(p.EvidenceFacts.Value) != len(EvidenceFacts) {
		facts = strings.Join(p.EvidenceFacts.Value, "+")
		if facts == "" {
			facts = "none"
		}
	}
	budget := "auto"
	if p.PromptBudgetTokens.Value > 0 {
		budget = strconv.FormatInt(p.PromptBudgetTokens.Value, 10)
	}
	escalate := "tier"
	switch {
	case p.EscalateFrom.Value == int64(keeper.HumanApprovalNever):
		escalate = "never"
	case p.EscalateFrom.Value > 0:
		escalate = fmt.Sprintf("L%d", p.EscalateFrom.Value)
	}
	return fmt.Sprintf("%s evidence=%s facts=%s hard_gate=%s escalate_from=%s precedent=%s/%d samples=%d budget=%s",
		p.Name.Value, onOff(p.Evidence.Value), facts, onOff(p.HardGate.Value), escalate,
		onOff(p.Precedent.Value), p.PrecedentN.Value, p.ConsistencySamples.Value, budget)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// resolveProfile layers the stored row over its preset over the built-in. Pure,
// like resolve() above it — this is the part worth testing exhaustively.
func resolveProfile(cur profileSettings) EffectiveProfile {
	name := DefaultProfile
	nameSource := SourceDefault
	if cur.profile != "" {
		name = ProfileName(cur.profile)
		nameSource = SourceInstance
	}
	base, ok := profilePresets[name]
	if !ok {
		// Unreachable through Apply (validate refuses an unknown name), but a
		// hand-edited row must not resolve to the zero profile — every capability
		// off, including the hard gate, is the one outcome that silently weakens
		// the judge. Fall back to the built-in and keep the stored name visible.
		base = profilePresets[DefaultProfile]
	}
	// A selected preset DECIDED every toggle it did not have overridden; the
	// built-in decided them otherwise. That distinction is the whole reason
	// SourceProfile exists.
	inherited := SourceDefault
	if nameSource == SourceInstance {
		inherited = SourceProfile
	}

	eff := EffectiveProfile{
		Name:       Field[string]{Value: string(name), Source: nameSource},
		Overridden: !cur.empty(),
	}
	eff.Evidence = pickBool(cur.evidence, base.evidence, inherited)
	eff.HardGate = pickBool(cur.hardGate, base.hardGate, inherited)
	eff.EscalateFrom = pickInt(cur.escalateFrom, base.escalateFrom, inherited)
	eff.Precedent = pickBool(cur.precedent, base.precedent, inherited)
	eff.PrecedentN = pickInt(cur.precedentN, base.precedentN, inherited)
	eff.ConsistencySamples = pickInt(cur.consistencySamples, base.consistencySamples, inherited)
	eff.PromptBudgetTokens = pickInt(cur.promptBudgetTokens, base.promptBudgetTokens, inherited)

	if facts := splitEvidenceFacts(cur.evidenceFacts); len(facts) > 0 {
		eff.EvidenceFacts = Field[[]string]{Value: facts, Source: SourceInstance}
	} else if len(base.evidenceFacts) > 0 {
		eff.EvidenceFacts = Field[[]string]{Value: base.evidenceFacts, Source: inherited}
	} else {
		// Resolved to the full list rather than to nil: see EffectiveProfile.
		eff.EvidenceFacts = Field[[]string]{Value: append([]string(nil), EvidenceFacts...), Source: inherited}
	}
	return eff
}

func pickBool(override *bool, base bool, inherited Source) Field[bool] {
	if override != nil {
		return Field[bool]{Value: *override, Source: SourceInstance}
	}
	return Field[bool]{Value: base, Source: inherited}
}

func pickInt(override *int64, base int64, inherited Source) Field[int64] {
	if override != nil {
		return Field[int64]{Value: *override, Source: SourceInstance}
	}
	return Field[int64]{Value: base, Source: inherited}
}

// splitEvidenceFacts parses the stored comma-separated list, trimming and
// dropping duplicates while preserving the order the operator typed. Comma
// separation rather than JSON because the fact keys are lower_snake_case by
// validation, so the split is unambiguous and the stored value, the CLI flag
// and the audit stamp are all the same string.
func splitEvidenceFacts(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(EvidenceFacts))
	for _, part := range strings.Split(raw, ",") {
		f := strings.ToLower(strings.TrimSpace(part))
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// applyProfilePatch folds the profile half of a Patch into the row being
// written. Splitting the patch by half keeps applyLocked readable; validation
// runs afterwards against the post-patch state, so an operator can select a
// profile and override one of its toggles in the same call.
func applyProfilePatch(next *profileSettings, p Patch) {
	if p.Profile != nil {
		next.profile = strings.ToLower(strings.TrimSpace(*p.Profile))
	}
	if p.EvidenceFacts != nil {
		// Normalised on the way in, so the stored value is what resolve returns
		// and the audit stamp cannot differ from the row by whitespace alone.
		next.evidenceFacts = strings.Join(splitEvidenceFacts(*p.EvidenceFacts), ",")
	}
	setTri(&next.evidence, p.Evidence)
	setTri(&next.hardGate, p.HardGate)
	setClearableInt(&next.escalateFrom, p.EscalateFrom)
	setTri(&next.precedent, p.Precedent)
	setClearableInt(&next.precedentN, p.PrecedentN)
	setClearableInt(&next.consistencySamples, p.ConsistencySamples)
	setClearableInt(&next.promptBudgetTokens, p.PromptBudgetTokens)
}

// setTri maps a tri-state patch field onto a nullable stored bool. An unknown
// TriBool cannot reach here — ParseTriBool and the API layer both reject it —
// so it is treated as "leave alone" rather than silently flipping a capability.
func setTri(dst **bool, patch *TriBool) {
	if patch == nil {
		return
	}
	switch *patch {
	case TriInherit:
		*dst = nil
	case TriOn:
		v := true
		*dst = &v
	case TriOff:
		v := false
		*dst = &v
	}
}

// setClearableInt writes a numeric override, treating 0 as "clear and inherit"
// — the convention the aux timeout already uses.
//
// It is safe for all three numbers here because none of them has 0 as a
// meaningful setting: one sample means self-consistency off (not zero), zero
// precedent examples means precedent off (the toggle), and a zero prompt budget
// is already spelled "no cap", which is what inheriting gives.
func setClearableInt(dst **int64, patch *int64) {
	if patch == nil {
		return
	}
	if *patch == 0 {
		*dst = nil
		return
	}
	v := *patch
	*dst = &v
}

// validateProfile checks the post-patch profile row. Errors are written for the
// operator who typed the value.
func validateProfile(p profileSettings) error {
	if p.profile != "" && !KnownProfile(ProfileName(p.profile)) {
		return newValidation(fmt.Sprintf("unknown judge profile %q — use one of %s",
			p.profile, joinProfiles()))
	}
	if p.evidenceFacts != "" {
		if len(p.evidenceFacts) > maxEvidenceFactsLength {
			return newValidation(fmt.Sprintf("the evidence fact list is too long (%d characters, limit %d)",
				len(p.evidenceFacts), maxEvidenceFactsLength))
		}
		for _, f := range splitEvidenceFacts(p.evidenceFacts) {
			if !KnownEvidenceFact(f) {
				// Refused rather than ignored: a typo that silently drops a fact
				// removes evidence from a security decision, and the judge fails
				// toward ALLOW when the evidence is missing.
				return newValidation(fmt.Sprintf("unknown evidence fact %q — the judge can compute %s "+
					`(clear the list with "" to use them all)`, f, strings.Join(EvidenceFacts, ", ")))
			}
		}
	}
	if p.precedentN != nil {
		if *p.precedentN < 1 || *p.precedentN > MaxPrecedentExamples {
			return newValidation(fmt.Sprintf(
				"the number of precedent examples must be between 1 and %d (set it to 0 to follow the profile, "+
					"or turn precedent off entirely)", MaxPrecedentExamples))
		}
	}
	if p.consistencySamples != nil {
		switch {
		case *p.consistencySamples < 1 || *p.consistencySamples > MaxConsistencySamples:
			return newValidation(fmt.Sprintf(
				"the number of consistency samples must be between 1 and %d — 1 means one verdict, "+
					"i.e. self-consistency off (set it to 0 to follow the profile)", MaxConsistencySamples))
		case *p.consistencySamples%2 == 0:
			// An even count has no majority: the extra sample only widens the
			// tie, and a tie escalates to a human. Refusing is cheaper than
			// letting an operator pay for a sample that buys nothing.
			return newValidation("the number of consistency samples must be odd — an even count has no majority, " +
				"so the extra sample only produces more ties")
		}
	}
	if p.escalateFrom != nil && *p.escalateFrom != 0 {
		// 1-4 or 0. Refused rather than clamped, because a typo'd 5 silently
		// clamping to L4 would put a human on every credential in the workspace
		// and the operator would have no idea why.
		if *p.escalateFrom < 1 || *p.escalateFrom > 5 {
			return newValidation("escalate-from must be a credential tier 1-4, 5 for never, or 0 to leave the tier table alone")
		}
	}
	if p.promptBudgetTokens != nil {
		if *p.promptBudgetTokens < MinPromptBudgetTokens || *p.promptBudgetTokens > MaxPromptBudgetTokens {
			return newValidation(fmt.Sprintf(
				"the prompt budget must be between %d and %d tokens (set it to 0 for no cap, "+
					"leaving the model server to decide what to drop)", MinPromptBudgetTokens, MaxPromptBudgetTokens))
		}
	}
	return nil
}

func joinProfiles() string {
	names := make([]string, 0, len(Profiles))
	for _, p := range Profiles {
		names = append(names, string(p))
	}
	return strings.Join(names, ", ")
}
