package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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
