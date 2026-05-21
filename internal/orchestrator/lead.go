package orchestrator

import (
	"fmt"
	"strings"
)

// MemberIntegration represents an MCP integration available to a crew member.
type MemberIntegration struct {
	Name       string   // display name, e.g. "Gmail"
	ServerName string   // machine name, e.g. "gmail"
	Tools      []string // tool names discovered from MCP server, e.g. ["gmail_send", "gmail_search"]
}

// CrewMember represents a fellow crew member visible to a lead agent.
type CrewMember struct {
	ID           string
	Name         string
	Slug         string
	RoleTitle    string
	Description  string
	Status       string
	ChatID       string
	Integrations []MemberIntegration
}

// leadContextStaticTail is the static orchestration cheat-sheet appended after
// the dynamic crew-member list. Collapsing ~50 per-call WriteString calls into
// a single raw string literal cuts both allocations and wall-clock time on
// every LEAD agent run.
const leadContextStaticTail = `
To assign a task to a crew member, use your bash tool:
  curl -s -X POST http://localhost:9119/assign \
    -H "Content-Type: application/json" \
    -d '{"target":"<slug>","task":"<description>"}'
To wait for and get the result:
  curl -s http://localhost:9119/results/<assignment_id>
(Poll /results/<id> until status is COMPLETED or FAILED.)

To ask a crew member a quick question (not a task):
  curl -s -X POST http://localhost:9119/query \
    -H "Content-Type: application/json" \
    -d '{"target":"<slug>","question":"<question>"}'

To get crew standup summary:
  curl -s http://localhost:9119/standup
  curl -s "http://localhost:9119/standup?since=2025-01-01T00:00:00Z"

TASK SCALING RULES (follow these when planning work):
Before assigning tasks, classify each by complexity:
  SIMPLE  — fact-finding, single operation, quick lookup
            → 1 agent, 3-10 tool calls, ~5 min, ~10K tokens
  MEDIUM  — comparison, multi-step, code changes in 1-2 files
            → 1-2 agents, 10-15 tool calls, ~15 min, ~50K tokens
  COMPLEX — research, multi-file changes, architecture decisions
            → 2-4 agents, 15+ tool calls, ~30 min, ~100K tokens
Match effort to complexity. Do NOT over-invest in simple tasks.
For SIMPLE tasks, prefer /assign (direct). For COMPLEX, use /mission/create.

STRUCTURED HANDOFF (required for all task outputs):
When you receive results from crew members, expect this structure:
  * summary: 1-3 sentence description of what was done
  * confidence: self-assessed quality (low/medium/high)
  * artifacts: list of files created or modified
If a result lacks summary or has low confidence, request clarification before proceeding.

To create a multi-task mission (advanced orchestration):
  curl -s -X POST http://localhost:9119/mission/create \
    -H "Content-Type: application/json" \
    -d '{"title":"...","description":"...","tasks":[
      {"title":"...","assigned_to":"<slug>","task_order":1},
      {"title":"...","assigned_to":"<slug>","task_order":2,"depends_on":["<task_id>"]}]}'
Then start it: curl -s -X POST http://localhost:9119/mission/<id>/start
Check status:  curl -s http://localhost:9119/mission/<id>
List templates: curl -s http://localhost:9119/mission/templates
Available templates: sequential, parallel, dev-test-loop, pipeline
Tasks with max_iterations will auto-retry on failure (Ralph Loop pattern).

CROSS-CREW MISSIONS:
Mission tasks can reference agents from connected crews.
The system auto-routes assignments to the correct crew container.
Crew connections must be established by workspace admins before use.

EPHEMERAL CONTRACTORS (PR-D F5 — when crew autonomy_level is trusted/full):
You can spawn a short-lived "contractor" agent for a single task.
Use this when:
  * The work needs a specialist (template) you don't currently have
  * The work is bounded — give it a TTL and it auto-ghosts when done
  * Spinning up a permanent agent would be overkill
Do NOT use this for ongoing work — hire a permanent agent instead.

  curl -s -X POST http://localhost:9119/spawn \
    -H "Content-Type: application/json" \
    -d '{"crew_slug":"<your-crew>","template_slug":"<from /crew-templates>",
         "model":"claude-haiku-4-5","ttl_minutes":60,
         "reason":"<one-sentence justification>"}'

Response includes the new agent_id; assign it work via /assign as usual.
Strict crews REJECT this call (autonomy_level=strict forbids ephemeral_spawn).
Guided crews block until an operator approves the hire in their inbox.
Trusted/full crews auto-spawn and log to the audit feed.
[END CREW CONTEXT]`

// BuildLeadContext formats a [CREW CONTEXT] block for the lead agent's system prompt.
// Returns empty string if there are no crew members (solo lead).
func BuildLeadContext(members []CrewMember) string {
	if len(members) == 0 {
		return ""
	}

	// Pre-size: [CREW CONTEXT] header + static tail + rough member budget.
	var b strings.Builder
	b.Grow(64 + len(leadContextStaticTail) + len(members)*128)

	b.WriteString("[CREW CONTEXT]\n")
	b.WriteString("Your fellow crew members:\n")

	for _, m := range members {
		if m.RoleTitle != "" {
			fmt.Fprintf(&b, "- %s (@%s, %s)", m.Name, m.Slug, m.RoleTitle)
		} else {
			fmt.Fprintf(&b, "- %s (@%s)", m.Name, m.Slug)
		}
		if m.Description != "" {
			fmt.Fprintf(&b, ": %s", m.Description)
		}
		b.WriteString("\n")
		if len(m.Integrations) > 0 {
			var parts []string
			for _, ig := range m.Integrations {
				if len(ig.Tools) > 0 {
					parts = append(parts, fmt.Sprintf("%s (%s)", ig.Name, strings.Join(ig.Tools, ", ")))
				} else {
					parts = append(parts, ig.Name)
				}
			}
			fmt.Fprintf(&b, "  Integrations: %s\n", strings.Join(parts, ", "))
		}
	}

	b.WriteString(leadContextStaticTail)
	return b.String()
}
