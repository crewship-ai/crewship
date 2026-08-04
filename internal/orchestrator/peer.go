package orchestrator

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/untrusted"
)

// peerContextQueryPrefix through peerContextTail hold the three static spans
// that surround the two selfSlug interpolations in the PEER COMMUNICATION
// block. Collapsing the previous ~10 WriteString + 2 string-concat calls into
// 5 direct WriteStrings saves allocations on every non-LEAD agent run.
const (
	peerContextQueryPrefix = `
To ask a crew member a question (auth comes from the fd-3 config — never -H, see SIDECAR AUTH):
  curl -s -X POST http://localhost:9119/query \
    -H "Content-Type: application/json" \
    -d '{"target":"<slug>","question":"<question>","from":"`

	peerContextEscalatePrefix = `"}' \
    -K /dev/fd/3 3<<AUTH
header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"
AUTH
The response will contain the crew member's answer.

To escalate to the lead (when you discover something needs a decision):
  curl -s -X POST http://localhost:9119/escalate \
    -H "Content-Type: application/json" \
    -d '{"from":"`

	peerContextTail = `","reason":"<why>","context":"<optional details>"}' \
    -K /dev/fd/3 3<<AUTH
header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"
AUTH
` + delegationBlock + `
[END PEER COMMUNICATION]`
)

// delegationBlock is the /assign recipe for a NON-lead agent, plus the cap it
// runs into. Shared verbatim with the lead cheat-sheet's tail so both prompts
// state the same limit — a cap described two ways is a cap described wrongly.
//
// Why a worker gets this at all: the endpoint was never lead-only in code (see
// internal/sidecar/assignment.go). Withholding the recipe made "leads
// orchestrate" true in practice while leaving it unenforced, which is the worst
// of both — no boundary, and no way for a specialist to hand off the one step
// it is the wrong agent for. The bound is now a server-side depth/fan-out cap
// that applies to leads and workers alike, so the recipe can be told honestly.
//
// The wording earns its length: the refusal arrives mid-task as a 403, and an
// agent that has never heard of the limit reports it as a broken product.
const delegationBlock = `
To hand a whole task to a crew member (not a question — /query is for questions):
  curl -s -X POST http://localhost:9119/assign \
    -H "Content-Type: application/json" \
    -d '{"target":"<slug>","task":"<description>"}' \
    -K /dev/fd/3 3<<AUTH
header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"
AUTH
Then poll: curl -s http://localhost:9119/results/<assignment_id>
(until status is COMPLETED or FAILED).
` + delegationLimitsNote

// delegationLimitsNote states the cap. It is appended to BOTH the lead
// cheat-sheet and the peer block: the limit applies to whoever calls /assign,
// so one text, used twice, rather than two that can drift apart.
const delegationLimitsNote = `
DELEGATION LIMITS — the server enforces these, not you:
  * DEPTH: work delegated to you may only be delegated on so many more times.
    Past the limit /assign is refused with 403 and a message naming it.
  * FAN-OUT: one run may only have so many sub-tasks out at once. Past the
    limit /assign is refused until some of them finish.
Delegate only what you are the wrong agent for; do the rest yourself. If you
are refused, that is policy working — say so, do the work yourself or report
back to whoever assigned it. Do not retry the same call, and do not route
around it by asking a peer to dispatch on your behalf.`

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
			// Fence the member's free-text description before it reaches a peer
			// agent's prompt — same ingress-trust boundary as the lead context
			// (#808 M1). Structural @slug/name/role stay unfenced.
			fmt.Fprintf(&b, ": %s", untrusted.Wrap("crew_member", m.Description))
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
