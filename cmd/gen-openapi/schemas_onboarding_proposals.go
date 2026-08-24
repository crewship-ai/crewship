package main

// onboardingProposalSchemaCatalog types the conversational-onboarding
// surface: the proposal store (create / get / apply) and the setup agent's
// session start.
//
// These four routes shipped with `{"type": "object"}` on both sides, which is
// the generator's way of saying "a handler returns JSON and nobody told me
// what". For this surface that is worse than usual: the proposal payload IS
// the contract — PRD §5.6 makes apply read only the stored payload, so what
// the card shows and what the mutation writes are the same struct, and a
// client has no way to know that from a bare `object`.
//
// Mirrors internal/api/onboarding_proposal.go (onboardingProposalRequest,
// onboardingProposalResponse, onboardingProposalPayload,
// onboardingProposalAgent) and internal/api/onboarding_setup_agent.go.
func onboardingProposalSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	nullableStr := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	components := map[string]any{
		// The roster the setup agent NAMED. Only these two fields are trusted
		// from the model; every operational field below is derived server-side.
		"OnboardingProposalAgentInput": object(map[string]any{
			"name": str(),
			"role": str(),
		}, "name", "role"),

		"OnboardingProposalRequest": object(map[string]any{
			"crew_name": str(),
			// Optional since the custom-roster path landed: a bespoke crew
			// with no matching builtin is a legitimate proposal. One of
			// template_slug or agents must be present.
			"template_slug": str(),
			"crew_slug":     str(),
			"llm_provider":  str(),
			"llm_model":     str(),
			"agents":        array(ref("OnboardingProposalAgentInput")),
			"tools":         array(str()),
		}, "crew_name"),

		// The RESOLVED agent — system_prompt and the rest are composed by the
		// server, never taken from the model's text.
		"OnboardingProposalAgent": object(map[string]any{
			"name":          str(),
			"slug":          str(),
			"role_title":    str(),
			"llm_provider":  str(),
			"llm_model":     str(),
			"system_prompt": str(),
		}, "name", "slug", "role_title"),

		"OnboardingProposalPayload": object(map[string]any{
			"crew_name":     str(),
			"crew_slug":     str(),
			"crew_icon":     nullableStr(),
			"template_slug": str(),
			"llm_provider":  str(),
			"llm_model":     str(),
			"mise_config":   str(),
			"tools":         array(str()),
			"agents":        array(ref("OnboardingProposalAgent")),
		}, "crew_name", "crew_slug", "agents"),

		"OnboardingProposal": object(map[string]any{
			"id":              str(),
			"workspace_id":    str(),
			"created_by":      str(),
			"created_at":      str(),
			"applied_at":      nullableStr(),
			"status":          str(),
			"payload":         ref("OnboardingProposalPayload"),
			"applied_crew_id": nullableStr(),
		}, "id", "workspace_id", "status", "payload"),

		// Apply is the only write in this surface, and it is idempotent:
		// already_applied distinguishes "we just built it" from "you already
		// had it", which is what lets a double-click be harmless.
		"OnboardingProposalApplyRequest": object(map[string]any{}),
		"OnboardingProposalApplyResponse": object(map[string]any{
			"proposal_id":     str(),
			"status":          str(),
			"already_applied": boolean(),
			"crew": object(map[string]any{
				"crew_id":     str(),
				"crew_name":   str(),
				"crew_slug":   str(),
				"agent_count": integer(),
				"agent_ids":   array(str()),
			}),
		}, "proposal_id", "status"),

		"OnboardingSetupAgentStartRequest": object(map[string]any{}),
		"OnboardingSetupAgentStartResponse": object(map[string]any{
			"agent_id":     str(),
			"agent_slug":   str(),
			"chat_id":      str(),
			"crew_id":      str(),
			"workspace_id": str(),
		}, "agent_id", "chat_id"),
	}

	routes := map[string]DomainSchema{
		"POST /api/v1/onboarding/proposals": {
			Request:  ref("OnboardingProposalRequest"),
			Response: ref("OnboardingProposal"),
		},
		"GET /api/v1/onboarding/proposals/{id}": {
			Response: ref("OnboardingProposal"),
		},
		"POST /api/v1/onboarding/proposals/{id}/apply": {
			Request:  ref("OnboardingProposalApplyRequest"),
			Response: ref("OnboardingProposalApplyResponse"),
		},
		"POST /api/v1/onboarding/setup-agent/start": {
			Request:  ref("OnboardingSetupAgentStartRequest"),
			Response: ref("OnboardingSetupAgentStartResponse"),
		},
	}
	return routes, components
}
