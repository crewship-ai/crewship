package api

import "strings"

// subAgentModelEnv is the operator opt-in that routes worker sub-agent runs to
// a cheaper model. Read via the injected getenv in resolveSubAgentModel so the
// resolution stays unit-testable.
const subAgentModelEnv = "CREWSHIP_SUBAGENT_MODEL"

// resolveSubAgentModel decides the model for a delegated sub-agent run.
//
// Worker (non-lead-planning) sub-agents execute bounded sub-tasks handed down
// by a lead and rarely need the top model tier, so when CREWSHIP_SUBAGENT_MODEL
// is set they run on that cheaper model. The lead planner is exempt — it does
// the actual reasoning/decomposition and keeps its configured model. When the
// env var is unset the target agent's own model is used unchanged, so this is a
// pure opt-in cost lever that never silently downgrades output quality.
func resolveSubAgentModel(targetModel string, leadPlanning bool, getenv func(string) string) string {
	if leadPlanning {
		return targetModel
	}
	if override := strings.TrimSpace(getenv(subAgentModelEnv)); override != "" {
		return override
	}
	return targetModel
}
