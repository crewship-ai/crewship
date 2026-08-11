package pipeline

// Governance risk-factor identifiers. A routine save is "risky" (and so
// lands as status='proposed' for human review) when it declares any of
// these. They double as the audit / inbox-payload reason strings so an
// operator can see *why* a routine needs approval.
const (
	// RiskHTTPStep — the routine contains an http/egress step (outbound
	// network call).
	RiskHTTPStep = "http_step"
	// RiskCodeStep — the routine contains a code-runtime step (executes a
	// script).
	RiskCodeStep = "code_step"
	// RiskEgressTargets — the routine declares routine-level egress_targets.
	RiskEgressTargets = "egress_targets"
	// RiskCredentialsRequired — the routine declares credentials_required.
	RiskCredentialsRequired = "credentials_required"
	// RiskUnmetIntegration — the routine declares an integrations_required
	// the author crew can't currently satisfy. Emitted by the API layer
	// (needs the crew's connected set) as "unmet_integration:<slug>".
	RiskUnmetIntegration = "unmet_integration"
)

// walkAllSteps calls visit once for every Step reachable from the DSL: the
// top-level list, the routine-level lifecycle hooks (before_all / after_all /
// on_failure), each step's own before/after hooks, and the body of every
// foreach step — recursively, so a hook on a body step and a body inside a body
// are both reached.
//
// One walk, two consumers (StaticRiskReasons and ExtractManifest), on purpose.
// Each used to carry its own copy of this recursion, identical line for line,
// and both copies were missing the foreach body. That is the shape of the
// defect: not a hard case handled wrongly, but the same easy case forgotten
// twice because nothing forced the two to agree. With one walk, a step kind
// that hides a capability can only be missed once.
//
// Why the foreach recursion is not optional: a foreach step reaches nothing
// itself — its BODY does. This branch made foreach saveable for the first time
// (dsl_validate_egress.go grew a StepForeach case; before it, every foreach
// routine was refused at save), so the omission stopped being latent. An http
// step wrapped in a foreach yielded NO risk reason, so the routine saved as
// `active` — live, never reviewed — while the byte-identical unwrapped step
// landed `proposed`. It matters most on the agent-authored door, which has
// neither a role gate nor a human between authorship and execution. The same
// blindness had ExtractManifest reporting has_http:false / egress:[] for a
// routine that calls out, i.e. the blast-radius screen asserting the opposite
// of the truth rather than merely omitting it.
//
// Termination: JSON cannot encode a cycle, but Foreach and Hooks are pointers
// and Go code can, and these two functions run on the save path where a hang is
// a dead API rather than a rejected routine. Each container is followed at most
// once. Skipping a repeat is safe rather than lossy because both visitors are
// idempotent — booleans that latch, slices that are deduped — so a body reached
// by two paths contributes the same whether it is walked once or twice. No
// depth ceiling: a cap would silently under-report a deep-but-legitimate
// routine, which is the exact failure being fixed here.
func walkAllSteps(d *DSL, visit func(*Step)) {
	if d == nil {
		return
	}
	seenForeach := map[*ForeachStep]struct{}{}
	seenHooks := map[*StepHooks]struct{}{}

	var scan func(st *Step)
	scan = func(st *Step) {
		if st == nil {
			return
		}
		visit(st)
		if fe := st.Foreach; fe != nil {
			if _, dup := seenForeach[fe]; !dup {
				seenForeach[fe] = struct{}{}
				for i := range fe.Steps {
					scan(&fe.Steps[i])
				}
			}
		}
		if h := st.Hooks; h != nil {
			if _, dup := seenHooks[h]; !dup {
				seenHooks[h] = struct{}{}
				scan(h.Before)
				scan(h.After)
			}
		}
	}

	for i := range d.Steps {
		scan(&d.Steps[i])
	}
	if d.Hooks != nil {
		scan(d.Hooks.BeforeAll)
		scan(d.Hooks.AfterAll)
		scan(d.Hooks.OnFailure)
	}
}

// StaticRiskReasons returns the governance risk factors derivable from the
// DSL *alone* (no DB, no crew context): any http step, any routine-level
// egress_targets, any code-runtime step, or any credentials_required entry.
//
// It walks every reachable step — top level, lifecycle hooks, and foreach
// bodies (see walkAllSteps). A routine whose visible steps are all agent_run
// but whose on_failure hook fires an http call is still egress-capable and must
// be reviewed; so is one whose only http call is inside a fan-out.
//
// An empty result means the routine is statically safe (only agent_run /
// transform / call_pipeline / wait, no egress, no credentials). The
// integration-satisfiability factor is layered on top in the API layer via
// RiskUnmetIntegration; see internal/api/pipeline_governance.go.
//
// Not reasons today, stated so the absence reads as a decision rather than an
// oversight: crewship steps (which write rows and can wake an agent) and script
// steps carry no factor of their own.
func (d *DSL) StaticRiskReasons() []string {
	if d == nil {
		return nil
	}
	var reasons []string
	if len(d.EgressTargets) > 0 {
		reasons = append(reasons, RiskEgressTargets)
	}

	hasHTTP, hasCode := false, false
	walkAllSteps(d, func(st *Step) {
		switch st.Type {
		case StepHTTP:
			hasHTTP = true
		case StepCode:
			hasCode = true
		}
	})

	if hasHTTP {
		reasons = append(reasons, RiskHTTPStep)
	}
	if hasCode {
		reasons = append(reasons, RiskCodeStep)
	}
	if len(d.CredsRequired) > 0 {
		reasons = append(reasons, RiskCredentialsRequired)
	}
	return reasons
}
