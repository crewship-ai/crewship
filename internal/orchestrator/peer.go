package orchestrator

import (
	"fmt"
	"strings"
)

// peerContextQueryPrefix through peerContextTail hold the three static spans
// that surround the two selfSlug interpolations in the PEER COMMUNICATION
// block. Collapsing the previous ~10 WriteString + 2 string-concat calls into
// 5 direct WriteStrings saves allocations on every non-LEAD agent run.
const (
	peerContextQueryPrefix = `
To ask a crew member a question:
  curl -s -X POST http://localhost:9119/query \
    -H "Content-Type: application/json" \
    -d '{"target":"<slug>","question":"<question>","from":"`

	peerContextEscalatePrefix = `"}'
The response will contain the crew member's answer.

To escalate to the lead (when you discover something needs a decision):
  curl -s -X POST http://localhost:9119/escalate \
    -H "Content-Type: application/json" \
    -d '{"from":"`

	peerContextTail = `","reason":"<why>","context":"<optional details>"}'
[END PEER COMMUNICATION]`
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
	// Pre-size: the three static spans dominate the total length; give the
	// member list a small budget on top so appending doesn't rehash.
	b.Grow(64 + len(peerContextQueryPrefix) + len(peerContextEscalatePrefix) +
		len(peerContextTail) + 2*len(selfSlug) + len(others)*96)

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

	b.WriteString(peerContextQueryPrefix)
	b.WriteString(selfSlug)
	b.WriteString(peerContextEscalatePrefix)
	b.WriteString(selfSlug)
	b.WriteString(peerContextTail)
	return b.String()
}
