package orchestrator

import (
	"fmt"
	"strings"
)

// BuildPeerContext formats a [PEER COMMUNICATION] block for non-lead agents
// that are part of a crew. This enables peer-to-peer Q&A between agents.
// Returns empty string if there are no other crew members.
func BuildPeerContext(members []CrewMember, selfSlug string) string {
	// Filter out self from the member list
	var others []CrewMember
	for _, m := range members {
		if m.Slug != selfSlug {
			others = append(others, m)
		}
	}
	if len(others) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[PEER COMMUNICATION]\n")
	b.WriteString("Your crew members:\n")

	for _, m := range others {
		if m.RoleTitle != "" {
			fmt.Fprintf(&b, "- %s (@%s, %s)", m.Name, m.Slug, m.RoleTitle)
		} else {
			fmt.Fprintf(&b, "- %s (@%s)", m.Name, m.Slug)
		}
		if m.Description != "" {
			fmt.Fprintf(&b, ": %s", m.Description)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString("To ask a crew member a question:\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/query \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"target\":\"<slug>\",\"question\":\"<question>\",\"from\":\"" + selfSlug + "\"}'\n")
	b.WriteString("The response will contain the crew member's answer.\n")
	b.WriteString("\n")
	b.WriteString("To escalate to the lead (when you discover something needs a decision):\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/escalate \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"from\":\"" + selfSlug + "\",\"reason\":\"<why>\",\"context\":\"<optional details>\"}'\n")

	b.WriteString("[END PEER COMMUNICATION]")
	return b.String()
}
