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
	b.WriteString("\n")
	b.WriteString("CROSS-CREW MISSIONS:\n")
	b.WriteString("Mission tasks can reference agents from connected crews.\n")
	b.WriteString("The system auto-routes assignments to the correct crew container.\n")
	b.WriteString("Crew connections must be established by workspace admins before use.\n")

	b.WriteString("[END CREW CONTEXT]")
	return b.String()
}

// CrewInfo represents a crew and its members for the coordinator context.
type CrewInfo struct {
	ID      string
	Name    string
	Slug    string
	Members []CrewMember
}

// MissionSummary is a lightweight mission status snapshot for coordinator context.
type MissionSummary struct {
	ID       string
	CrewSlug string
	Title    string
	Status   string
}

// BuildCoordinatorContext formats a [COORDINATOR CONTEXT] block listing all
// workspace crews and their agents. The coordinator can create cross-crew
// missions, submit proposals for human review, and monitor all workspace missions.
func BuildCoordinatorContext(crews []CrewInfo, activeMissions []MissionSummary) string {
	if len(crews) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[COORDINATOR CONTEXT]\n")
	b.WriteString("You are the workspace Coordinator (CEO). You see ALL crews and their agents.\n")
	b.WriteString("Your role: decompose complex objectives into crew-specific missions,\n")
	b.WriteString("submit proposals for human approval, and monitor cross-crew progress.\n\n")

	for _, c := range crews {
		b.WriteString(fmt.Sprintf("## Crew: %s (@%s)\n", c.Name, c.Slug))
		if len(c.Members) == 0 {
			b.WriteString("  (no agents)\n")
		}
		for _, m := range c.Members {
			if m.RoleTitle != "" {
				b.WriteString(fmt.Sprintf("  - %s (@%s, %s)", m.Name, m.Slug, m.RoleTitle))
			} else {
				b.WriteString(fmt.Sprintf("  - %s (@%s)", m.Name, m.Slug))
			}
			if m.Description != "" {
				b.WriteString(fmt.Sprintf(": %s", m.Description))
			}
			b.WriteString(fmt.Sprintf(" [crew_id=%s]\n", c.ID))
		}
		b.WriteString("\n")
	}

	// Active missions summary
	if len(activeMissions) > 0 {
		b.WriteString("ACTIVE MISSIONS:\n")
		for _, m := range activeMissions {
			b.WriteString(fmt.Sprintf("  [%s] %s (crew: @%s, id: %s)\n", m.Status, m.Title, m.CrewSlug, m.ID))
		}
		b.WriteString("\n")
	}

	b.WriteString("PROPOSAL WORKFLOW (recommended for multi-crew objectives):\n")
	b.WriteString("Submit a proposal for human review before creating missions:\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/proposal \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"title\":\"...\",\"description\":\"...\",\"plan\":\"...\",\"missions\":[\n")
	b.WriteString("      {\"crew_id\":\"<id>\",\"title\":\"...\",\"tasks\":[\n")
	b.WriteString("        {\"title\":\"...\",\"assigned_agent_id\":\"<id>\",\"task_order\":1}]},\n")
	b.WriteString("      {\"crew_id\":\"<id>\",\"title\":\"...\",\"tasks\":[...]}]}'\n")
	b.WriteString("The human will approve/reject your proposal. On approval, missions are auto-created.\n\n")

	b.WriteString("Check proposal status:  curl -s http://localhost:9119/proposals\n")
	b.WriteString("Monitor all missions:   curl -s http://localhost:9119/missions/all\n")
	b.WriteString("Mission summary:        curl -s http://localhost:9119/missions/all/summary\n\n")

	b.WriteString("DIRECT MISSION (for urgent tasks, no human approval):\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/mission/create \\\n")
	b.WriteString("    -H \"Content-Type: application/json\" \\\n")
	b.WriteString("    -d '{\"title\":\"...\",\"crew_id\":\"<home_crew_id>\",\"tasks\":[\n")
	b.WriteString("      {\"title\":\"...\",\"assigned_to_id\":\"<agent_id>\",\"task_order\":1}]}'\n")
	b.WriteString("Then start: curl -s -X POST http://localhost:9119/mission/<id>/start\n\n")

	b.WriteString("OTHER COMMANDS:\n")
	b.WriteString("List crew connections:  curl -s http://localhost:9119/crew-connections\n")
	b.WriteString("List all crews:         curl -s http://localhost:9119/crews\n")
	b.WriteString("Check mission status:   curl -s http://localhost:9119/mission/<id>\n")
	b.WriteString("List templates:         curl -s http://localhost:9119/mission/templates\n")

	b.WriteString("[END COORDINATOR CONTEXT]")
	return b.String()
}
