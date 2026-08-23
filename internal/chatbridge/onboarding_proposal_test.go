package chatbridge

import "testing"

func TestOnboardingProposalMetadata(t *testing.T) {
	t.Parallel()
	valid := `I recommend the Support Crew.
<!-- crewship:onboarding-proposal {"crew_name":"Support Crew","crew_slug":"support-crew","template_slug":"customer-support","llm_provider":"anthropic","llm_model":"claude-sonnet-5"} -->`
	meta := onboardingProposalMetadata(onboardingSetupAgentSlug, valid)
	if meta == nil {
		t.Fatal("valid setup-agent marker produced no metadata")
	}
	suggestion, ok := meta[onboardingProposalMetadataKey].(onboardingProposalSuggestion)
	if !ok {
		t.Fatalf("suggestion type = %T", meta[onboardingProposalMetadataKey])
	}
	if suggestion.CrewName != "Support Crew" || suggestion.TemplateSlug != "customer-support" {
		t.Fatalf("suggestion = %+v", suggestion)
	}
	if suggestion.LLMProvider != "ANTHROPIC" {
		t.Errorf("provider = %q, want ANTHROPIC", suggestion.LLMProvider)
	}
}

func TestOnboardingProposalMetadataRejectsUntrustedOrMalformedMarkers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		agentSlug string
		text      string
	}{
		{"ordinary agent", "support-agent", `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support"} -->`},
		{"invalid json", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal nope -->`},
		{"missing name", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"template_slug":"customer-support"} -->`},
		{"unsafe template", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"../secret"} -->`},
		{"unfinished marker", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := onboardingProposalMetadata(tt.agentSlug, tt.text); got != nil {
				t.Fatalf("metadata = %#v, want nil", got)
			}
		})
	}
}
