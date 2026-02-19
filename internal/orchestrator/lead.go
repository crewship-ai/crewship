package orchestrator

import (
	"fmt"
	"strings"
)

// CrewMember represents a fellow crew member visible to a lead agent.
type CrewMember struct {
	Name        string
	Slug        string
	RoleTitle   string
	Description string
	Status      string
}

// BuildLeadContext formats a [CREW CONTEXT] block for the lead agent's system prompt.
// Returns empty string if there are no crew members (solo lead).
func BuildLeadContext(members []CrewMember) string {
	if len(members) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[CREW CONTEXT]\n")
	b.WriteString("Your fellow crew members:\n")

	for _, m := range members {
		if m.RoleTitle != "" {
			b.WriteString(fmt.Sprintf("- %s (@%s, %s)", m.Name, m.Slug, m.RoleTitle))
		} else {
			b.WriteString(fmt.Sprintf("- %s (@%s)", m.Name, m.Slug))
		}
		if m.Description != "" {
			b.WriteString(fmt.Sprintf(": %s", m.Description))
		}
		b.WriteString("\n")
	}

	b.WriteString("[END CREW CONTEXT]")
	return b.String()
}
