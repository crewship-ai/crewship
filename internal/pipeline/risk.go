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

// StaticRiskReasons returns the governance risk factors derivable from the
// DSL *alone* (no DB, no crew context): any http step, any routine-level
// egress_targets, any code-runtime step, or any credentials_required entry.
//
// It walks the top-level steps AND the lifecycle hooks (routine-level
// before_all/after_all/on_failure and per-step before/after) — a routine
// whose visible steps are all agent_run but whose on_failure hook fires an
// http call is still egress-capable and must be reviewed.
//
// An empty result means the routine is statically safe (only agent_run /
// transform / call_pipeline / wait, no egress, no credentials). The
// integration-satisfiability factor is layered on top in the API layer via
// RiskUnmetIntegration; see internal/api/pipeline_governance.go.
func (d *DSL) StaticRiskReasons() []string {
	if d == nil {
		return nil
	}
	var reasons []string
	if len(d.EgressTargets) > 0 {
		reasons = append(reasons, RiskEgressTargets)
	}

	hasHTTP, hasCode := false, false
	var scan func(st *Step)
	scan = func(st *Step) {
		if st == nil {
			return
		}
		switch st.Type {
		case StepHTTP:
			hasHTTP = true
		case StepCode:
			hasCode = true
		}
		if st.Hooks != nil {
			scan(st.Hooks.Before)
			scan(st.Hooks.After)
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
