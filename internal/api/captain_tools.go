package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
)

// CaptainTools is the set of tool definitions passed to the LLM on every Captain request.
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
func strInput(input map[string]any, key string) string {
	v, _ := input[key].(string)
	return v
}

func execGetWorkspaceStats(ctx context.Context, h *CaptainHandler, wsID, userID, role string, _ map[string]any) (string, error) {
	var crews, agents, missions, escalations, proposals int
	err := h.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS'),
			(SELECT COUNT(*) FROM escalations WHERE workspace_id = ? AND status = 'PENDING'),
			(SELECT COUNT(*) FROM mission_proposals WHERE workspace_id = ? AND status = 'PENDING')`,
		wsID, wsID, wsID, wsID, wsID).Scan(&crews, &agents, &missions, &escalations, &proposals)
	if err != nil {
		return "", fmt.Errorf("count workspace stats: %w", err)
	}
	return fmt.Sprintf("Workspace stats:\n- Crews: %d\n- Agents: %d\n- Active missions: %d\n- Pending escalations: %d\n- Pending proposals: %d",
		crews, agents, missions, escalations, proposals), nil
}

func execListCrews(ctx context.Context, h *CaptainHandler, wsID, _, _ string, _ map[string]any) (string, error) {
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, name, slug, description FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name", wsID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		ID   string  `json:"id"`
		Name string  `json:"name"`
		Slug string  `json:"slug"`
		Desc *string `json:"description,omitempty"`
	}
	var result []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Slug, &r.Desc); err != nil {
			return "", fmt.Errorf("scan crew: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate crews: %w", err)
	}
	if len(result) == 0 {
		return "No crews found.", nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func execListAgents(ctx context.Context, h *CaptainHandler, wsID, _, _ string, input map[string]any) (string, error) {
	query := "SELECT id, name, slug, role_title, agent_role, crew_id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL"
	args := []any{wsID}
	if crewID := strInput(input, "crew_id"); crewID != "" {
		query += " AND crew_id = ?"
		args = append(args, crewID)
	}
	query += " ORDER BY name"
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Slug      string  `json:"slug"`
		RoleTitle *string `json:"role_title,omitempty"`
		AgentRole string  `json:"agent_role"`
		CrewID    *string `json:"crew_id,omitempty"`
	}
	var result []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Slug, &r.RoleTitle, &r.AgentRole, &r.CrewID); err != nil {
			return "", fmt.Errorf("scan agent: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate agents: %w", err)
	}
	if len(result) == 0 {
		return "No agents found.", nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func execListCredentials(ctx context.Context, h *CaptainHandler, wsID, _, _ string, _ map[string]any) (string, error) {
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, name, provider, type, status FROM credentials WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name", wsID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Status   string `json:"status"`
	}
	var result []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Provider, &r.Type, &r.Status); err != nil {
			return "", fmt.Errorf("scan credential: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate credentials: %w", err)
	}
	if len(result) == 0 {
		return "No credentials found.", nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func execListMissions(ctx context.Context, h *CaptainHandler, wsID, _, _ string, input map[string]any) (string, error) {
	query := "SELECT id, title, status, crew_id, created_at FROM missions WHERE workspace_id = ?"
	args := []any{wsID}
	if status := strInput(input, "status"); status != "" {
		query += " AND status = ?"
		args = append(args, strings.ToUpper(status))
	}
	query += " ORDER BY created_at DESC LIMIT 20"
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		CrewID    string `json:"crew_id"`
		CreatedAt string `json:"created_at"`
	}
	var result []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Title, &r.Status, &r.CrewID, &r.CreatedAt); err != nil {
			return "", fmt.Errorf("scan mission: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate missions: %w", err)
	}
	if len(result) == 0 {
		return "No missions found.", nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func execListEscalations(ctx context.Context, h *CaptainHandler, wsID, _, _ string, _ map[string]any) (string, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, type, content, status, created_at FROM escalations
		WHERE workspace_id = ? AND status = 'PENDING'
		ORDER BY created_at DESC LIMIT 20`, wsID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Content   string `json:"content"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	var result []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Type, &r.Content, &r.Status, &r.CreatedAt); err != nil {
			return "", fmt.Errorf("scan escalation: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate escalations: %w", err)
	}
	if len(result) == 0 {
		return "No pending escalations.", nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func execCreateCrew(ctx context.Context, h *CaptainHandler, wsID, _, role string, input map[string]any) (string, error) {
	if !canRole(role, "create") {
		return "", fmt.Errorf("insufficient permissions")
	}
	name := strInput(input, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	slug := strInput(input, "slug")
	if slug == "" {
		slug = slugify(name)
	} else {
		slug = slugify(slug)
	}
	if slug == "" {
		return "", fmt.Errorf("slug must contain only lowercase letters, numbers, and hyphens")
	}
	var existing int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL", slug, wsID).Scan(&existing); err != nil {
		return "", fmt.Errorf("check crew slug: %w", err)
	}
	if existing > 0 {
		return "", fmt.Errorf("crew with slug '%s' already exists", slug)
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(ctx, `
		INSERT INTO crews (id, workspace_id, name, slug, description, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, name, slug,
		nilIfEmpty(strInput(input, "description")),
		nilIfEmpty(strInput(input, "icon")),
		nilIfEmpty(strInput(input, "color")),
		now, now)
	if err != nil {
		return "", fmt.Errorf("create crew: %w", err)
	}
	b, _ := json.Marshal(map[string]string{"id": id, "name": name, "slug": slug})
	return string(b), nil
}

func execCreateAgent(ctx context.Context, h *CaptainHandler, wsID, _, role string, input map[string]any) (string, error) {
	if !canRole(role, "create") {
		return "", fmt.Errorf("insufficient permissions")
	}
	name := strInput(input, "name")
	crewID := strInput(input, "crew_id")
	if name == "" || crewID == "" {
		return "", fmt.Errorf("name and crew_id are required")
	}

	if err := crewExists(ctx, h.db, crewID, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("crew %q not found in workspace", crewID)
		}
		return "", fmt.Errorf("check crew existence: %w", err)
	}

	slug := strInput(input, "slug")
	if slug == "" {
		slug = slugify(name)
	} else {
		slug = slugify(slug)
	}
	if slug == "" {
		return "", fmt.Errorf("name must contain at least one letter or number to generate a valid slug")
	}
	// Ensure workspace uniqueness with a simple suffix if taken.
	var taken int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agents WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL", slug, wsID).Scan(&taken); err != nil {
		return "", fmt.Errorf("check agent slug: %w", err)
	}
	if taken > 0 {
		slug = slug + "-" + generateCUID()[:6]
	}

	agentRole := strings.ToUpper(strInput(input, "agent_role"))
	if agentRole == "" {
		agentRole = "AGENT"
	}
	if agentRole != "AGENT" && agentRole != "LEAD" {
		return "", fmt.Errorf("agent_role must be AGENT or LEAD, got %q", agentRole)
	}
	toolProfile := strInput(input, "tool_profile")
	if toolProfile == "" {
		toolProfile = "CODING"
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(ctx, `
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, role_title, agent_role,
			cli_adapter, tool_profile, system_prompt, timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'CLAUDE_CODE', ?, ?, 1800, 1, ?, ?, ?)`,
		id, wsID, crewID, name, slug,
		nilIfEmpty(strInput(input, "role_title")),
		agentRole, toolProfile,
		nilIfEmpty(strInput(input, "system_prompt")),
		generateWebhookSecret(), now, now)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}

	// Auto-assign workspace AI credentials so the agent can run immediately.
	autoAssignCredentials(ctx, h.db, wsID, id, now)

	b, _ := json.Marshal(map[string]string{"id": id, "name": name, "slug": slug, "agent_role": agentRole})
	return string(b), nil
}

func execCreateMission(ctx context.Context, h *CaptainHandler, wsID, _, role string, input map[string]any) (string, error) {
	if !canRole(role, "create") {
		return "", fmt.Errorf("insufficient permissions")
	}
	crewID := strInput(input, "crew_id")
	title := strInput(input, "title")
	if crewID == "" || title == "" {
		return "", fmt.Errorf("crew_id and title are required")
	}

	if err := crewExists(ctx, h.db, crewID, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("crew %q not found", crewID)
		}
		return "", fmt.Errorf("check crew: %w", err)
	}

	var leadID string
	err := h.db.QueryRowContext(ctx,
		"SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1", crewID).Scan(&leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no LEAD agent in crew %q", crewID)
	}
	if err != nil {
		return "", fmt.Errorf("lookup LEAD agent: %w", err)
	}

	missionID := generateCUID()
	traceID := "mission-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, scope, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', 'workspace', ?, ?)`,
		missionID, wsID, crewID, leadID, traceID, title,
		nilIfEmpty(strInput(input, "description")), now, now); err != nil {
		return "", fmt.Errorf("insert mission: %w", err)
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO chats (id, workspace_id, agent_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		missionID, wsID, leadID, "Mission: "+title, now, now, now); err != nil {
		return "", fmt.Errorf("insert chat: %w", err)
	}

	taskOrder := 0
	if tasksRaw, ok := input["tasks"].([]any); ok {
		for _, tr := range tasksRaw {
			tm, _ := tr.(map[string]any)
			taskTitle, _ := tm["title"].(string)
			if taskTitle == "" {
				continue
			}
			if ord, ok := tm["task_order"].(float64); ok {
				taskOrder = int(ord)
			}
			taskDesc, _ := tm["description"].(string)
			agentID, _ := tm["assigned_agent_id"].(string)
			var assignedAgent *string
			if agentID != "" {
				assignedAgent = &agentID
			}
			taskID := generateCUID()
			if _, err = tx.ExecContext(ctx, `
				INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'PENDING', ?, '[]', ?, ?)`,
				taskID, missionID, assignedAgent, taskTitle, nilIfEmpty(taskDesc), taskOrder, now, now); err != nil {
				return "", fmt.Errorf("insert task: %w", err)
			}
			taskOrder++
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	// Update status to IN_PROGRESS after successful commit.
	if _, err := h.db.ExecContext(ctx,
		"UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339), missionID); err != nil {
		return "", fmt.Errorf("update mission status: %w", err)
	}

	if h.missionEngine != nil {
		if startErr := h.missionEngine.StartMission(ctx, missionID); startErr != nil {
			// StartMission failed — mark as FAILED so we don't leave a dangling IN_PROGRESS.
			if _, updErr := h.db.ExecContext(ctx,
				"UPDATE missions SET status = 'FAILED', updated_at = ? WHERE id = ?",
				time.Now().UTC().Format(time.RFC3339), missionID); updErr != nil {
				h.logger.Error("failed to set mission FAILED after engine error", "mission_id", missionID, "error", updErr)
			}
			b, _ := json.Marshal(map[string]string{"id": missionID, "status": "FAILED", "warning": "engine start failed: " + startErr.Error()})
			return string(b), nil
		}
	}

	b, _ := json.Marshal(map[string]string{"id": missionID, "status": "IN_PROGRESS"})
	return string(b), nil
}

func execApproveProposal(ctx context.Context, h *CaptainHandler, wsID, userID, role string, input map[string]any) (string, error) {
	if !canRole(role, "manage") {
		return "", fmt.Errorf("insufficient permissions")
	}
	proposalID := strInput(input, "proposal_id")
	if proposalID == "" {
		return "", fmt.Errorf("proposal_id is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	notes := strInput(input, "review_notes")

	res, err := h.db.ExecContext(ctx, `
		UPDATE mission_proposals SET status = 'APPROVED', reviewed_by = ?, reviewed_at = ?, review_notes = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'`,
		userID, now, nilIfEmpty(notes), now, proposalID, wsID)
	if err != nil {
		return "", fmt.Errorf("approve proposal: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("check rows affected: %w", err)
	}
	if affected == 0 {
		return "", fmt.Errorf("proposal %q not found or not in PENDING state", proposalID)
	}

	rollbackProposal := func() {
		if _, rollErr := h.db.ExecContext(ctx,
			"UPDATE mission_proposals SET status = 'PENDING', reviewed_by = NULL, reviewed_at = NULL, review_notes = NULL, updated_at = ? WHERE id = ?",
			now, proposalID); rollErr != nil {
			h.logger.Error("rollback proposal status", "error", rollErr)
		}
	}

	var missionsJSON string
	if err := h.db.QueryRowContext(ctx,
		"SELECT missions_json FROM mission_proposals WHERE id = ?", proposalID).Scan(&missionsJSON); err != nil {
		rollbackProposal()
		return "", fmt.Errorf("load proposal missions: %w", err)
	}

	var missions []proposalMission
	if err := json.Unmarshal([]byte(missionsJSON), &missions); err != nil {
		rollbackProposal()
		return "", fmt.Errorf("parse missions: %w", err)
	}

	missionIDs, err := createMissionsFromProposal(ctx, h.db, wsID, proposalID, missions)
	if err != nil {
		rollbackProposal()
		return "", fmt.Errorf("create missions: %w", err)
	}

	b, _ := json.Marshal(map[string]any{"proposal_id": proposalID, "status": "APPROVED", "mission_ids": missionIDs})
	return string(b), nil
}

func execApplyCrewTemplate(ctx context.Context, h *CaptainHandler, wsID, _, role string, input map[string]any) (string, error) {
	if !canRole(role, "create") {
		return "", fmt.Errorf("insufficient permissions")
	}
	templateSlug := strInput(input, "template_slug")
	crewName := strInput(input, "crew_name")
	if templateSlug == "" || crewName == "" {
		return "", fmt.Errorf("template_slug and crew_name are required")
	}

	result, err := deployCrewTemplate(ctx, h.db, wsID, templateSlug, crewName, strInput(input, "crew_slug"))
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}
