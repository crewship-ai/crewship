package api

import (
	"strings"
	"testing"
)

// The crew designer prompt asks the model for agent_role="LEAD" / "AGENT",
// and a prompt is not a validator. validateSuggestion counted LEADs and never
// looked at what the *other* agents claimed, so a model answering the retired
// COORDINATOR — a plausible completion, the word "coordinates" sits in the
// instruction for the sibling role — produced a suggestion the wizard
// rendered as a lineup and POST /api/v1/agents then refused with
// 400 "agent_role must be AGENT or LEAD" (#2197).
//
// #2192's lesson applied to this surface: guard the *property* over a
// vocabulary of roles rather than pinning the one literal already known to be
// wrong. The property is that a suggestion may only carry roles the create
// endpoint accepts — whatever those turn out to be — so the accepted set is
// read from validAgentRoles, never restated here.

// knownAgentRoleNames is every agent_role token this product has named plus
// the near-misses a language model is most likely to invent for the same
// slot. Each is judged against validAgentRoles, not against a literal, so a
// role added to (or removed from) the API changes what these tests demand
// without an edit here.
var knownAgentRoleNames = []string{
	// Supported today — internal/api/agents.go validAgentRoles.
	"AGENT",
	"LEAD",
	// Retired in v0.1 and still referenced across the codebase: the CLI
	// (#2189), the create-agent dialog copy (#2166), the manifest kind
	// (#2195), docs/concepts.mdx's replacement record.
	"COORDINATOR",
	// Never existed. Plausible completions for a slot described as "this one
	// coordinates the crew's work" — the failure mode is the class, not the
	// one token we happened to observe.
	"ORCHESTRATOR",
	"SUPERVISOR",
	"MANAGER",
	"WORKER",
}

// suggestionWithRole builds an otherwise-valid two-agent suggestion whose
// first agent carries role, keeping exactly one LEAD so the role check is the
// only thing under test.
func suggestionWithRole(role string) *AISuggestResponse {
	other := "LEAD"
	if strings.EqualFold(strings.TrimSpace(role), "LEAD") {
		other = "AGENT"
	}
	return &AISuggestResponse{
		CrewName: "Docs Crew",
		CrewSlug: "docs-crew",
		Agents: []AISuggestedAgent{
			{Name: "Candidate", Slug: "candidate", RoleTitle: "Candidate", AgentRole: role, SystemPrompt: "You do the thing."},
			{Name: "Other", Slug: "other", RoleTitle: "Other", AgentRole: other, SystemPrompt: "You do the other thing."},
		},
	}
}

// Without this, adding a role to validAgentRoles would silently shrink what
// the table below covers: an unlisted role is neither exercised as accepted
// nor as refused. Failing here is the prompt to extend knownAgentRoleNames.
func TestSuggestedRole_VocabularyCoversEveryAcceptedRole(t *testing.T) {
	for role := range validAgentRoles {
		found := false
		for _, known := range knownAgentRoleNames {
			if known == role {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("validAgentRoles accepts %q but knownAgentRoleNames does not list it — extend the vocabulary", role)
		}
	}
}

func TestSuggestedRole_OnlyRolesTheCreateEndpointAcceptsSurvive(t *testing.T) {
	// Casing and padding are variants of the same token, not separate roles:
	// the model produces prose-shaped JSON and POST /api/v1/agents compares
	// exactly, so a suggestion is only deliverable once the token is
	// canonical. Whatever validateSuggestion returns must be a literal
	// validAgentRoles accepts.
	variants := func(role string) []string {
		if role == "" {
			return []string{""}
		}
		lower := strings.ToLower(role)
		mixed := strings.ToUpper(lower[:1]) + lower[1:]
		return []string{role, lower, mixed, " " + role + " "}
	}

	type tc struct {
		name string
		role string
	}
	var cases []tc
	for _, role := range knownAgentRoleNames {
		for _, v := range variants(role) {
			cases = append(cases, tc{name: role + "/" + strings.ReplaceAll(v, " ", "_"), role: v})
		}
	}
	// An omitted agent_role is the create endpoint's own default (AGENT), not
	// an unsupported role — agents_create.go fills it in the same way.
	cases = append(cases, tc{name: "omitted", role: ""})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			canonical := strings.ToUpper(strings.TrimSpace(c.role))
			if canonical == "" {
				canonical = "AGENT"
			}
			wantAccepted := validAgentRoles[canonical]

			s := suggestionWithRole(c.role)
			err := validateSuggestion(s)

			if wantAccepted {
				if err != nil {
					t.Fatalf("role %q is accepted by POST /api/v1/agents; validateSuggestion refused it: %v", c.role, err)
				}
				// The post-condition that makes the wizard's preview honest:
				// every role it is about to render is a role the form can
				// actually submit.
				for _, a := range s.Agents {
					if !validAgentRoles[a.AgentRole] {
						t.Errorf("agent %q left validation with agent_role %q, which POST /api/v1/agents refuses", a.Name, a.AgentRole)
					}
				}
				return
			}

			if err == nil {
				t.Fatalf("role %q is refused by POST /api/v1/agents with 400; validateSuggestion accepted the suggestion", c.role)
			}
			// The refusal is read in a server log, so it has to say which
			// value was wrong and what the accepted ones are — a bare
			// "validation failed" sends the reader to the LLM's raw output.
			if !strings.Contains(strings.ToUpper(err.Error()), canonical) {
				t.Errorf("refusal must name the offending role %q, got %q", c.role, err)
			}
			for accepted := range validAgentRoles {
				if !strings.Contains(strings.ToUpper(err.Error()), accepted) {
					t.Errorf("refusal must name %q as an accepted role, got %q", accepted, err)
				}
			}
		})
	}
}

// The role check is an addition, not a replacement: the exactly-one-LEAD rule
// this function has always enforced still has to hold.
func TestSuggestedRole_ExactlyOneLeadStillRequired(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
	}{
		{"no lead", []string{"AGENT", "AGENT"}},
		{"two leads", []string{"LEAD", "LEAD"}},
		{"two leads, mixed casing", []string{"LEAD", "lead"}},
		{"three agents, no lead", []string{"AGENT", "AGENT", "AGENT"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &AISuggestResponse{CrewName: "X", CrewSlug: "x"}
			for i, role := range c.roles {
				s.Agents = append(s.Agents, AISuggestedAgent{
					Name:         "A" + string(rune('1'+i)),
					Slug:         "a" + string(rune('1'+i)),
					AgentRole:    role,
					SystemPrompt: "p",
				})
			}
			if err := validateSuggestion(s); err == nil {
				t.Errorf("roles %v must be refused: a crew needs exactly one LEAD", c.roles)
			}
		})
	}
}

// The one-agent-in-many case: a five-agent crew where a single non-lead
// carries a retired role is still a crew the wizard must not offer.
func TestSuggestedRole_OneBadRoleAmongManyIsRefused(t *testing.T) {
	s := &AISuggestResponse{
		CrewName: "Big Crew", CrewSlug: "big-crew",
		Agents: []AISuggestedAgent{
			{Name: "L", Slug: "l", AgentRole: "LEAD", SystemPrompt: "p"},
			{Name: "A1", Slug: "a1", AgentRole: "AGENT", SystemPrompt: "p"},
			{Name: "A2", Slug: "a2", AgentRole: "AGENT", SystemPrompt: "p"},
			{Name: "C", Slug: "c", AgentRole: "COORDINATOR", SystemPrompt: "p"},
		},
	}
	if err := validateSuggestion(s); err == nil {
		t.Fatal("a suggestion containing one COORDINATOR must be refused whole; the wizard creates all of its agents or none")
	}
}
