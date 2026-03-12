package orchestrator

import (
	"fmt"
	"strings"
)

// CrewMember represents a fellow crew member visible to a lead agent.
type CrewMember struct {
	ID          string
	Name        string
	Slug        string
	RoleTitle   string
	Description string
	Status      string
	ChatID      string
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

	b.WriteString("\n")
	b.WriteString("To assign a task to a crew member, use your bash tool:\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/assign \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"target\":\"<slug>\",\"task\":\"<description>\"}'\n")
	b.WriteString("To wait for and get the result:\n")
	b.WriteString("  curl -s http://localhost:9119/results/<assignment_id>\n")
	b.WriteString("(Poll /results/<id> until status is COMPLETED or FAILED.)\n")
	b.WriteString("\n")
	b.WriteString("To ask a crew member a quick question (not a task):\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/query \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"target\":\"<slug>\",\"question\":\"<question>\"}'\n")
	b.WriteString("\n")
	b.WriteString("To get crew standup summary:\n")
	b.WriteString("  curl -s http://localhost:9119/standup\n")
	b.WriteString("  curl -s \"http://localhost:9119/standup?since=2025-01-01T00:00:00Z\"\n")
	b.WriteString("\n")
	b.WriteString("To create a multi-task mission (advanced orchestration):\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/mission/create \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"title\":\"...\",\"description\":\"...\",\"tasks\":[\n")
	b.WriteString("      {\"title\":\"...\",\"assigned_to\":\"<slug>\",\"task_order\":1},\n")
	b.WriteString("      {\"title\":\"...\",\"assigned_to\":\"<slug>\",\"task_order\":2,\"depends_on\":[\"<task_id>\"]}]}'\n")
	b.WriteString("Then start it: curl -s -X POST http://localhost:9119/mission/<id>/start\n")
	b.WriteString("Check status:  curl -s http://localhost:9119/mission/<id>\n")
	b.WriteString("List templates: curl -s http://localhost:9119/mission/templates\n")
	b.WriteString("Available templates: sequential, parallel, dev-test-loop, pipeline\n")
	b.WriteString("Tasks with max_iterations will auto-retry on failure (Ralph Loop pattern).\n")

	b.WriteString("[END CREW CONTEXT]")
	return b.String()
}
