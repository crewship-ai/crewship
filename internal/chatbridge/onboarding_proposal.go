package chatbridge

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	// onboardingSetupAgentSlug is reserved by the API when it creates the
	// temporary onboarding agent. Keep this deliberately narrow: ordinary
	// agents must never be able to smuggle onboarding-only metadata into a
	// chat turn merely by printing the marker below.
	onboardingSetupAgentSlug = "_crewship-setup-guide"

	onboardingProposalMarkerStart = "<!-- crewship:onboarding-proposal "
	onboardingProposalMarkerEnd   = " -->"
	onboardingProposalMetadataKey = "onboarding_proposal_suggestion"
)

var onboardingTemplateSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// onboardingProposalSuggestion is intentionally smaller than a proposal.
// The setup agent may choose a builtin template and name the crew, but the
// authenticated proposal endpoint resolves the actual roster from the
// database. No agent-authored names, prompts or permissions are trusted.
type onboardingProposalSuggestion struct {
	CrewName     string `json:"crew_name"`
	CrewSlug     string `json:"crew_slug,omitempty"`
	TemplateSlug string `json:"template_slug"`
	LLMProvider  string `json:"llm_provider,omitempty"`
	LLMModel     string `json:"llm_model,omitempty"`
}

// onboardingProposalMetadata extracts the final hidden proposal marker from
// a completed setup-agent response. It returns the exact wire shape the web
// client already consumes. Malformed output is ignored rather than promoted
// into a proposal card; the user can keep talking or choose the always-visible
// template fallback.
func onboardingProposalMetadata(agentSlug, text string) map[string]any {
	if agentSlug != onboardingSetupAgentSlug {
		return nil
	}
	start := strings.LastIndex(text, onboardingProposalMarkerStart)
	if start < 0 {
		return nil
	}
	jsonStart := start + len(onboardingProposalMarkerStart)
	relEnd := strings.Index(text[jsonStart:], onboardingProposalMarkerEnd)
	if relEnd < 0 {
		return nil
	}
	raw := text[jsonStart : jsonStart+relEnd]
	var suggestion onboardingProposalSuggestion
	if err := json.Unmarshal([]byte(raw), &suggestion); err != nil {
		return nil
	}
	suggestion.CrewName = strings.TrimSpace(suggestion.CrewName)
	suggestion.CrewSlug = strings.TrimSpace(suggestion.CrewSlug)
	suggestion.TemplateSlug = strings.TrimSpace(suggestion.TemplateSlug)
	suggestion.LLMProvider = strings.ToUpper(strings.TrimSpace(suggestion.LLMProvider))
	suggestion.LLMModel = strings.TrimSpace(suggestion.LLMModel)
	if suggestion.CrewName == "" || len(suggestion.CrewName) > 120 ||
		!onboardingTemplateSlugRe.MatchString(suggestion.TemplateSlug) {
		return nil
	}
	if suggestion.CrewSlug != "" && !onboardingTemplateSlugRe.MatchString(suggestion.CrewSlug) {
		return nil
	}
	return map[string]any{onboardingProposalMetadataKey: suggestion}
}
