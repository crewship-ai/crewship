package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

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

	found, err := crewExists(ctx, h.db, crewID, wsID)
	if err != nil {
		return "", fmt.Errorf("check crew existence: %w", err)
	}
	if !found {
		return "", fmt.Errorf("crew %q not found in workspace", crewID)
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

	_, err = h.db.ExecContext(ctx, `
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
	autoAssignCredentials(ctx, h.db, h.logger, wsID, id, now)

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

	found, err := crewExists(ctx, h.db, crewID, wsID)
	if err != nil {
		return "", fmt.Errorf("check crew: %w", err)
	}
	if !found {
		return "", fmt.Errorf("crew %q not found", crewID)
	}

	var leadID string
	err = h.db.QueryRowContext(ctx,
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

	result, err := deployCrewTemplate(ctx, h.db, h.logger, wsID, templateSlug, crewName, strInput(input, "crew_slug"))
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}
