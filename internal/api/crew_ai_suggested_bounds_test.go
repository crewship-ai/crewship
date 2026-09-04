package api

import (
	"strings"
	"testing"
)

// Follow-up to #2197 / #2200 (see crew_ai_suggested_role_test.go), one field
// over: validateSuggestion gave agent_role a post-condition — every value an
// accepted suggestion carries is a literal POST /api/v1/agents will accept —
// but never extended the same guarantee to name and slug. agents_create.go
// enforces name 2-100 bytes and slug 2-50 bytes (agentNameMinLen /
// agentNameMaxLen / agentSlugMinLen / agentSlugMaxLen in agents.go);
// validateSuggestion checked only Name == "" and never capped the slug
// slugify derives, so a suggestion like {"name":"Q", …} sailed through and
// the wizard's next call died with 400 "name must be 2-100 characters"
// (#2204).
//
// Every case here is judged against the shared constants, never against a
// restated literal, so the two cannot drift out from under this test.

// suggestionWithAgentNameAndSlug builds an otherwise-valid two-agent
// suggestion (one LEAD, one AGENT, both with a non-empty system prompt) with
// the first agent's Name and Slug set to the given values, isolating name /
// slug bounds as the only thing under test.
func suggestionWithAgentNameAndSlug(name, slug string) *AISuggestResponse {
	return &AISuggestResponse{
		CrewName: "Docs Crew",
		CrewSlug: "docs-crew",
		Agents: []AISuggestedAgent{
			{Name: name, Slug: slug, RoleTitle: "Candidate", AgentRole: "LEAD", SystemPrompt: "You do the thing."},
			{Name: "Other Agent", Slug: "other-agent", RoleTitle: "Other", AgentRole: "AGENT", SystemPrompt: "You do the other thing."},
		},
	}
}

func TestValidateSuggestion_NameBounds_MirrorTheCreateEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		agent    string // the AISuggestedAgent.Name under test
		accepted bool
	}{
		{"one byte, below minimum", "Q", false},
		{"empty", "", false},
		{"at minimum", strings.Repeat("x", agentNameMinLen), true},
		{"one below minimum", strings.Repeat("x", agentNameMinLen-1), false},
		{"at maximum", strings.Repeat("x", agentNameMaxLen), true},
		{"one over maximum", strings.Repeat("x", agentNameMaxLen+1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := suggestionWithAgentNameAndSlug(c.agent, "candidate")
			err := validateSuggestion(s)
			if c.accepted && err != nil {
				t.Errorf("name of length %d bytes is accepted by POST /api/v1/agents; validateSuggestion refused it: %v", len(c.agent), err)
			}
			if !c.accepted && err == nil {
				t.Errorf("name of length %d bytes is refused by POST /api/v1/agents with 400; validateSuggestion accepted it", len(c.agent))
			}
		})
	}
}

func TestValidateSuggestion_SlugBounds_MirrorTheCreateEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		slug     string
		accepted bool
	}{
		{"one byte, below minimum", "x", false},
		{"at minimum", strings.Repeat("x", agentSlugMinLen), true},
		{"at maximum", strings.Repeat("x", agentSlugMaxLen), true},
		{"one over maximum", strings.Repeat("x", agentSlugMaxLen+1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Name stays comfortably in-bounds so only the slug is under
			// test.
			s := suggestionWithAgentNameAndSlug("Candidate Agent", c.slug)
			err := validateSuggestion(s)
			if c.accepted && err != nil {
				t.Errorf("slug of length %d bytes is accepted by POST /api/v1/agents; validateSuggestion refused it: %v", len(c.slug), err)
			}
			if !c.accepted && err == nil {
				t.Errorf("slug of length %d bytes is refused by POST /api/v1/agents with 400; validateSuggestion accepted it", len(c.slug))
			}
		})
	}
}

// A model naming an agent with a long, space-free name is not exotic — the
// slug slugify derives from it can overflow the create endpoint's 50-byte
// slug cap even though the 100-byte name cap has plenty of room left. The
// derived slug must be judged too, not just the literal one the model sent.
func TestValidateSuggestion_LongNameWithNoExplicitSlug_DerivedSlugStillBounded(t *testing.T) {
	longName := strings.Repeat("a", agentSlugMaxLen+1) // 51 bytes: within name bounds, over slug bounds
	if len(longName) > agentNameMaxLen {
		t.Fatalf("test fixture invalid: longName is %d bytes, name cap is %d", len(longName), agentNameMaxLen)
	}
	s := suggestionWithAgentNameAndSlug(longName, "") // no explicit slug: falls back to slugify(name)
	err := validateSuggestion(s)
	if err == nil {
		t.Fatalf("a name whose derived slug (%d bytes) exceeds the %d-byte slug cap must be refused; validateSuggestion accepted it", len(longName), agentSlugMaxLen)
	}
}

// The post-condition validateSuggestion is meant to hold: whatever survives
// validation is a name/slug pair POST /api/v1/agents actually accepts.
func TestValidateSuggestion_Accepted_NameAndSlugAreWithinCreateBounds(t *testing.T) {
	s := suggestionWithAgentNameAndSlug("Candidate Agent", "candidate-agent")
	if err := validateSuggestion(s); err != nil {
		t.Fatalf("validateSuggestion: %v", err)
	}
	for _, a := range s.Agents {
		if l := len(a.Name); l < agentNameMinLen || l > agentNameMaxLen {
			t.Errorf("agent %q left validation with a %d-byte name, outside %d-%d", a.Name, l, agentNameMinLen, agentNameMaxLen)
		}
		if l := len(a.Slug); l < agentSlugMinLen || l > agentSlugMaxLen {
			t.Errorf("agent %q left validation with a %d-byte slug %q, outside %d-%d", a.Name, l, a.Slug, agentSlugMinLen, agentSlugMaxLen)
		}
	}
}
