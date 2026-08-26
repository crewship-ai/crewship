package chatbridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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

func TestOnboardingProposalMetadataWithCustomAgents(t *testing.T) {
	t.Parallel()
	valid := `I recommend a single-agent Web Monitoring Crew.
<!-- crewship:onboarding-proposal {"crew_name":"Web Monitoring Crew","crew_slug":"web-monitoring","template_slug":"devops-sre","llm_provider":"anthropic","llm_model":"claude-sonnet-5","agents":[{"name":"Monitoring Engineer","role":"Monitors uptime and alerts on failures"}]} -->`
	meta := onboardingProposalMetadata(onboardingSetupAgentSlug, valid)
	if meta == nil {
		t.Fatal("valid setup-agent marker with agents produced no metadata")
	}
	suggestion, ok := meta[onboardingProposalMetadataKey].(onboardingProposalSuggestion)
	if !ok {
		t.Fatalf("suggestion type = %T", meta[onboardingProposalMetadataKey])
	}
	if len(suggestion.Agents) != 1 {
		t.Fatalf("agents = %+v, want 1 entry", suggestion.Agents)
	}
	if suggestion.Agents[0].Name != "Monitoring Engineer" || suggestion.Agents[0].Role != "Monitors uptime and alerts on failures" {
		t.Fatalf("agent = %+v", suggestion.Agents[0])
	}
}

func TestOnboardingProposalMetadataWithoutAnyTemplate(t *testing.T) {
	t.Parallel()
	valid := `I recommend a bespoke crew with no matching template.
<!-- crewship:onboarding-proposal {"crew_name":"Bespoke Crew","llm_provider":"anthropic","llm_model":"claude-sonnet-5","agents":[{"name":"Solo Agent","role":"Does the whole job"}]} -->`
	meta := onboardingProposalMetadata(onboardingSetupAgentSlug, valid)
	if meta == nil {
		t.Fatal("valid template-free marker produced no metadata")
	}
	suggestion := meta[onboardingProposalMetadataKey].(onboardingProposalSuggestion)
	if suggestion.TemplateSlug != "" {
		t.Errorf("template_slug = %q, want empty", suggestion.TemplateSlug)
	}
	if len(suggestion.Agents) != 1 {
		t.Fatalf("agents = %+v, want 1 entry", suggestion.Agents)
	}
}

// TestOnboardingProposalMetadataAcceptsNonASCIIRoles is the regression guard
// for a bug that made the product work in English and break in Czech.
//
// The length check was `len(role) > 80`, and len() on a Go string counts
// BYTES. A Czech role sentence carries 8-15 multi-byte runes, so the real
// ceiling was ~66 characters, not 80 — and an over-long role discarded the
// ENTIRE marker via `return nil`, silently, with nothing logged. In the
// session that found this, the first two proposals passed with five bytes of
// headroom and every one after them vanished: the user watched the Guide
// describe a crew and no card ever appeared.
//
// The role below is the literal string from that session.
func TestOnboardingProposalMetadataAcceptsNonASCIIRoles(t *testing.T) {
	t.Parallel()
	const role = "Sleduje pravidelná ozvání z uživatelova počítače a hlásí výpadek i obnovení dostupnosti"
	if len(role) <= onboardingProposalNameMaxLen {
		t.Fatalf("fixture no longer exercises the bug: %d bytes is under the %d-byte ceiling",
			len(role), onboardingProposalNameMaxLen)
	}

	text := `Návrh posádky.
<!-- crewship:onboarding-proposal {"crew_name":"Hlídka mého PC","crew_slug":"hlidka-meho-pc","llm_provider":"ANTHROPIC","llm_model":"claude-haiku-4-5","agents":[{"name":"Hlídač dostupnosti","role":"` + role + `"}]} -->`

	meta := onboardingProposalMetadata(onboardingSetupAgentSlug, text)
	if meta == nil {
		t.Fatal("a Czech role sentence discarded the whole proposal — the card never appears")
	}
	s := meta[onboardingProposalMetadataKey].(onboardingProposalSuggestion)
	if len(s.Agents) != 1 {
		t.Fatalf("agents = %+v, want 1", s.Agents)
	}
	if s.Agents[0].Role == "" {
		t.Error("role was dropped entirely")
	}
}

// A role longer than the ceiling is TRUNCATED, never grounds for discarding
// the proposal. Losing the tail of one sentence is a cosmetic loss; losing the
// marker means the user cannot create the crew at all.
func TestOnboardingProposalMetadataTruncatesAnOverlongRole(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("ř", onboardingProposalRoleMaxLen+50)
	text := `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support","agents":[{"name":"A","role":"` + long + `"}]} -->`

	meta := onboardingProposalMetadata(onboardingSetupAgentSlug, text)
	if meta == nil {
		t.Fatal("an over-long role discarded the proposal instead of being trimmed")
	}
	s := meta[onboardingProposalMetadataKey].(onboardingProposalSuggestion)
	if got := utf8.RuneCountInString(s.Agents[0].Role); got > onboardingProposalRoleMaxLen {
		t.Errorf("role kept %d runes, ceiling is %d", got, onboardingProposalRoleMaxLen)
	}
}

// The NAME stays a hard reject and stays counted in runes: a name is a label
// on a card, and a 200-character one is a malformed marker, not a long
// sentence.
func TestOnboardingProposalMetadataCountsNameInRunesNotBytes(t *testing.T) {
	t.Parallel()
	// 70 accented runes = 140 bytes: legal by runes, would have failed by bytes.
	name := strings.Repeat("á", 70)
	text := `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support","agents":[{"name":"` + name + `","role":"R"}]} -->`
	if onboardingProposalMetadata(onboardingSetupAgentSlug, text) == nil {
		t.Error("a 70-rune name was rejected — the ceiling is still counting bytes")
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
		{"agent missing name", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support","agents":[{"role":"Lead"}]} -->`},
		{"agent missing role", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support","agents":[{"name":"Lead"}]} -->`},
		{"too many agents", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"customer-support","agents":[{"name":"A","role":"R"},{"name":"B","role":"R"},{"name":"C","role":"R"},{"name":"D","role":"R"},{"name":"E","role":"R"},{"name":"F","role":"R"},{"name":"G","role":"R"}]} -->`},
		{"no template and no agents", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X"} -->`},
		{"unsafe template with no agents fallback", onboardingSetupAgentSlug, `<!-- crewship:onboarding-proposal {"crew_name":"X","template_slug":"../secret"} -->`},
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

// The same byte-vs-rune bug as TestOnboardingProposalMetadataAcceptsNonASCIIRoles,
// one field over. The role ceiling was fixed to count runes; crew_name kept a
// `len() > 120` byte check, and a crew name is the field most likely to be
// written in the user's own language. A Czech, Greek or Japanese name over 120
// BYTES — roughly 60 accented characters, well inside what the field is meant
// to allow — discarded the entire marker via a silent `return nil`. Same
// symptom as before: the Guide describes a crew, no card ever appears, nothing
// is logged.
func TestOnboardingProposalMetadataCountsCrewNameInRunesNotBytes(t *testing.T) {
	t.Parallel()
	// 70 accented runes = 140 bytes: legal by runes, fatal by bytes.
	name := strings.Repeat("ě", 70)
	if len(name) <= onboardingProposalCrewNameMaxLen {
		t.Fatalf("fixture no longer exercises the bug: %d bytes is under the %d ceiling",
			len(name), onboardingProposalCrewNameMaxLen)
	}
	text := `<!-- crewship:onboarding-proposal {"crew_name":"` + name + `","template_slug":"customer-support"} -->`
	if onboardingProposalMetadata(onboardingSetupAgentSlug, text) == nil {
		t.Error("a 70-rune crew name was rejected — the ceiling is still counting bytes")
	}
}

// Genuinely over the ceiling is still a hard reject, unlike a role: a crew
// name is a LABEL on a card, and a 200-character one is a malformed marker
// rather than a long sentence that can be trimmed.
func TestOnboardingProposalMetadataRejectsAnOverlongCrewName(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("ě", onboardingProposalCrewNameMaxLen+1)
	text := `<!-- crewship:onboarding-proposal {"crew_name":"` + name + `","template_slug":"customer-support"} -->`
	if onboardingProposalMetadata(onboardingSetupAgentSlug, text) != nil {
		t.Error("a crew name past the rune ceiling was accepted")
	}
}
