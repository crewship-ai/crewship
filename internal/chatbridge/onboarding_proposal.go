package chatbridge

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
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

	// onboardingProposalMaxAgents bounds how many agents the setup agent can
	// name in one proposal. A hard ceiling, not a target — most proposals
	// should size the roster to the task, which is often one agent.
	onboardingProposalMaxAgents = 6
	// RUNES, not bytes. `len()` on a Go string counts bytes, and this ceiling
	// used to be applied that way: a Czech role sentence carries 8-15
	// multi-byte runes, so the effective limit was ~66 characters and the
	// third proposal in a Czech conversation silently discarded its whole
	// marker. The product worked in English and broke in Czech.
	onboardingProposalNameMaxLen = 80
	// A role is a SENTENCE ("watches uptime and reports outages and
	// recoveries"), a name is a LABEL. They had one ceiling and it was sized
	// for the label, which is what made a perfectly reasonable role fatal.
	onboardingProposalRoleMaxLen = 200
	// A crew name is the field most likely to be written in the user's own
	// language, and it was the last one still counted in BYTES after the role
	// ceiling was fixed — 120 bytes is about 60 accented characters, so a
	// perfectly ordinary Czech or Greek crew name discarded the whole marker.
	// Runes, like the two above.
	onboardingProposalCrewNameMaxLen = 120
	// Mirrors internal/api's onboardingProposalMaxTools. Two constants because
	// this package must not import the api package; the api side re-checks.
	onboardingProposalMaxTools = 5
)

var onboardingTemplateSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// A palette id ("blue") or a six-digit hex, with or without '#'. Anything
// else is dropped before it reaches the card.
var onboardingCrewColorRe = regexp.MustCompile(`^(#?[0-9a-fA-F]{6}|[a-z]{3,12})$`)

// onboardingToolNameRe is the shape a mise tool id can take. Same character
// class MiseConfig.Validate enforces at build time, applied early so a name
// carrying a path traversal or a shell metacharacter never travels further.
var onboardingToolNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// onboardingProposalAgentSuggestion is one agent identity (name + role) the
// setup agent named in its prose. Only these two fields are trusted from the
// agent's own text — the proposal endpoint still derives every operational
// field (model, adapter, tool profile, system prompt) itself.
type onboardingProposalAgentSuggestion struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// onboardingProposalSuggestion is intentionally smaller than a proposal.
// The setup agent may choose a builtin template, name the crew, and name the
// agents it described, but the authenticated proposal endpoint resolves the
// actual roster (system prompts, tool profiles, adapters) from the database
// and its own trusted derivation. No agent-authored prompts or permissions
// are trusted.
type onboardingProposalSuggestion struct {
	CrewName string `json:"crew_name"`
	CrewSlug string `json:"crew_slug,omitempty"`
	// CrewIcon / CrewColor: the Guide's pick for the crew's look. Shape only
	// here (a kebab-case icon name; a palette id or a hex) — membership in the
	// real icon vocabulary is checked by the API, which owns that list.
	CrewIcon     string                              `json:"crew_icon,omitempty"`
	CrewColor    string                              `json:"crew_color,omitempty"`
	TemplateSlug string                              `json:"template_slug"`
	LLMProvider  string                              `json:"llm_provider,omitempty"`
	LLMModel     string                              `json:"llm_model,omitempty"`
	Agents       []onboardingProposalAgentSuggestion `json:"agents,omitempty"`
	// Tools are runtime NAMES only (mise tool ids). The server resolves each
	// against a closed catalogue and pins the version itself — nothing here
	// reaches a container build unmapped, and a name the catalogue does not
	// know is dropped rather than installed. Deliberately not a version map:
	// the narrower this field, the less a prompt injection in a scraped page
	// can ask the container to become.
	Tools []string `json:"tools,omitempty"`
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
	suggestion.CrewIcon = strings.TrimSpace(suggestion.CrewIcon)
	if suggestion.CrewIcon != "" && !onboardingTemplateSlugRe.MatchString(suggestion.CrewIcon) {
		suggestion.CrewIcon = ""
	}
	suggestion.CrewColor = strings.TrimSpace(suggestion.CrewColor)
	if suggestion.CrewColor != "" && !onboardingCrewColorRe.MatchString(suggestion.CrewColor) {
		suggestion.CrewColor = ""
	}
	if suggestion.CrewName == "" || utf8.RuneCountInString(suggestion.CrewName) > onboardingProposalCrewNameMaxLen {
		return nil
	}
	// template_slug is optional once agents are named directly — a bespoke
	// crew with no matching builtin template is a legitimate proposal, not
	// a malformed one. When given, it still has to look like a real slug.
	if suggestion.TemplateSlug != "" && !onboardingTemplateSlugRe.MatchString(suggestion.TemplateSlug) {
		return nil
	}
	if suggestion.TemplateSlug == "" && len(suggestion.Agents) == 0 {
		return nil
	}
	if suggestion.CrewSlug != "" && !onboardingTemplateSlugRe.MatchString(suggestion.CrewSlug) {
		return nil
	}
	if len(suggestion.Agents) > onboardingProposalMaxAgents {
		return nil
	}
	// Shape only — membership against the runtime catalogue is the API's job
	// (resolveProposalTool), because this package cannot import it. A name
	// that is obviously not a tool id is dropped here so the rest never sees
	// it; a plausible-but-unknown one is dropped there.
	if len(suggestion.Tools) > onboardingProposalMaxTools {
		return nil
	}
	cleanTools := make([]string, 0, len(suggestion.Tools))
	for _, t := range suggestion.Tools {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len(t) > 50 || !onboardingToolNameRe.MatchString(t) {
			continue
		}
		cleanTools = append(cleanTools, t)
	}
	suggestion.Tools = cleanTools
	for i, a := range suggestion.Agents {
		name := strings.TrimSpace(a.Name)
		role := strings.TrimSpace(a.Role)
		// A missing field, or a name too long to be a label, is a malformed
		// marker and the proposal is dropped.
		if name == "" || role == "" || utf8.RuneCountInString(name) > onboardingProposalNameMaxLen {
			return nil
		}
		// An over-long ROLE is trimmed rather than fatal. Losing the tail of
		// one sentence costs the user nothing they will notice; losing the
		// marker means no card appears and the crew cannot be created at all,
		// with nothing logged to explain it. That asymmetry is the whole
		// lesson of the bug this replaces.
		if r := []rune(role); len(r) > onboardingProposalRoleMaxLen {
			role = strings.TrimSpace(string(r[:onboardingProposalRoleMaxLen]))
		}
		suggestion.Agents[i] = onboardingProposalAgentSuggestion{Name: name, Role: role}
	}
	return map[string]any{onboardingProposalMetadataKey: suggestion}
}
