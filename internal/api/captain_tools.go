package api

// File: captain_tools.go — 11 tool executors for the deprecated Captain feature.
//
// DEPRECATED (2026-04-16): See captain.go file header for context.

import (
	"context"

	"github.com/crewship-ai/crewship/internal/llm"
)

// CaptainTools is the set of tool definitions passed to the LLM on every Captain request.
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
var CaptainTools = []llm.ToolDef{
	{
		Name:        "get_workspace_stats",
		Description: "Get an overview of the workspace: number of crews, agents, active missions, pending escalations, and pending proposals.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "list_crews",
		Description: "List all crews in the workspace.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "list_agents",
		Description: "List agents in the workspace, optionally filtered by crew_id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"crew_id": map[string]any{"type": "string", "description": "Filter agents by crew ID (optional)"},
			},
		},
	},
	{
		Name:        "list_credentials",
		Description: "List credentials in the workspace. Returns id, name, provider, type, status — never the secret value.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "list_missions",
		Description: "List missions filtered by status (PLANNING, IN_PROGRESS, COMPLETED, FAILED, CANCELLED). Returns up to 20 most recent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string", "description": "Mission status filter (optional)"},
			},
		},
	},
	{
		Name:        "list_escalations",
		Description: "List pending escalations that require human attention.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "create_crew",
		Description: "Create a new crew in the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Crew name (required)"},
				"slug":        map[string]any{"type": "string", "description": "URL-friendly slug (auto-generated from name if omitted)"},
				"description": map[string]any{"type": "string", "description": "Crew description (optional)"},
				"icon":        map[string]any{"type": "string", "description": "Emoji icon (optional)"},
				"color":       map[string]any{"type": "string", "description": "Hex color (optional)"},
			},
			"required": []string{"name"},
		},
	},
	{
		Name:        "create_agent",
		Description: "Create a new agent and add it to a crew.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Agent name (required)"},
				"crew_id":       map[string]any{"type": "string", "description": "Crew ID to add the agent to (required)"},
				"slug":          map[string]any{"type": "string", "description": "URL-friendly slug (auto-generated if omitted)"},
				"role_title":    map[string]any{"type": "string", "description": "Role title, e.g. 'Backend Engineer'"},
				"agent_role":    map[string]any{"type": "string", "description": "AGENT or LEAD (default: AGENT)"},
				"system_prompt": map[string]any{"type": "string", "description": "Agent's system prompt / persona"},
				"tool_profile":  map[string]any{"type": "string", "description": "Tool profile (default: CODING)"},
			},
			"required": []string{"name", "crew_id"},
		},
	},
	{
		Name:        "create_mission",
		Description: "Create and immediately start a mission for a crew. Requires a LEAD agent in the crew.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"crew_id":     map[string]any{"type": "string", "description": "Crew ID to run the mission (required)"},
				"title":       map[string]any{"type": "string", "description": "Mission title (required)"},
				"description": map[string]any{"type": "string", "description": "Mission description (optional)"},
				"tasks": map[string]any{
					"type":        "array",
					"description": "Tasks for the mission",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":             map[string]any{"type": "string"},
							"description":       map[string]any{"type": "string"},
							"assigned_agent_id": map[string]any{"type": "string"},
							"task_order":        map[string]any{"type": "integer"},
						},
						"required": []string{"title"},
					},
				},
			},
			"required": []string{"crew_id", "title"},
		},
	},
	{
		Name:        "approve_proposal",
		Description: "Approve a pending mission proposal and start the missions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"proposal_id":  map[string]any{"type": "string", "description": "Proposal ID to approve (required)"},
				"review_notes": map[string]any{"type": "string", "description": "Optional review notes"},
			},
			"required": []string{"proposal_id"},
		},
	},
	{
		Name:        "apply_crew_template",
		Description: "Deploy a crew template to create a pre-configured crew with agents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template_slug": map[string]any{"type": "string", "description": "Template slug (required)"},
				"crew_name":     map[string]any{"type": "string", "description": "Name for the new crew (required)"},
				"crew_slug":     map[string]any{"type": "string", "description": "URL slug (auto-generated if omitted)"},
			},
			"required": []string{"template_slug", "crew_name"},
		},
	},
}

// captainToolExecutor is the function signature for all Captain tool executors.
type captainToolExecutor func(ctx context.Context, h *CaptainHandler, wsID, userID, role string, input map[string]any) (string, error)

var captainToolExecutors = map[string]captainToolExecutor{
	"get_workspace_stats": execGetWorkspaceStats,
	"list_crews":          execListCrews,
	"list_agents":         execListAgents,
	"list_credentials":    execListCredentials,
	"list_missions":       execListMissions,
	"list_escalations":    execListEscalations,
	"create_crew":         execCreateCrew,
	"create_agent":        execCreateAgent,
	"create_mission":      execCreateMission,
	"approve_proposal":    execApproveProposal,
	"apply_crew_template": execApplyCrewTemplate,
}

// strInput safely extracts a string value from a tool input map.
