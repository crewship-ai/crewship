package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

type InternalHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	internalToken string
}

func NewInternalHandler(db *sql.DB, internalToken string, logger *slog.Logger) *InternalHandler {
	return &InternalHandler{db: db, internalToken: internalToken, logger: logger}
}

func (h *InternalHandler) requireInternal(next http.Handler) http.Handler {
	if h.internalToken == "" {
		h.logger.Error("internal token is empty -- all internal API calls will be rejected")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Internal-Token")
		if h.internalToken == "" || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.internalToken)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *InternalHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	provider := r.URL.Query().Get("provider")

	query := `SELECT id, workspace_id, name, type, provider, encrypted_value,
		encrypted_refresh_token, token_expires_at, account_label, account_email, status
		FROM credentials
		WHERE status = 'ACTIVE' AND deleted_at IS NULL
		AND type IN ('AI_CLI_TOKEN', 'API_KEY') AND provider != 'NONE'`

	var args []interface{}
	if workspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, workspaceID)
	}
	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY type ASC, created_at ASC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("internal list credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type credResult struct {
		ID           string  `json:"id"`
		WorkspaceID  string  `json:"workspace_id"`
		Name         string  `json:"name"`
		Type         string  `json:"type"`
		Provider     string  `json:"provider"`
		AccessToken  string  `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		TokenExpires *string `json:"token_expires_at"`
		AccountLabel *string `json:"account_label"`
		Status       string  `json:"status"`
	}

	var result []credResult
	for rows.Next() {
		var c credResult
		var encValue string
		var encRefresh, accountEmail sql.NullString
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Type, &c.Provider,
			&encValue, &encRefresh, &c.TokenExpires, &c.AccountLabel, &accountEmail, &c.Status); err != nil {
			h.logger.Error("scan internal credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		decrypted, err := encryption.Decrypt(encValue)
		if err != nil {
			h.logger.Error("decrypt credential", "id", c.ID, "error", err)
			continue
		}
		c.AccessToken = decrypted
		if encRefresh.Valid {
			rt, err := encryption.Decrypt(encRefresh.String)
			if err != nil {
				h.logger.Debug("decrypt refresh token", "id", c.ID, "error", err)
			} else {
				c.RefreshToken = &rt
			}
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (internal credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []credResult{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *InternalHandler) UpdateCredentialStatus(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")

	var body struct {
		Status       string  `json:"status"`
		LastError    *string `json:"last_error"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		TokenExpires *string `json:"token_expires_at"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	validStatuses := map[string]bool{
		"ACTIVE": true, "EXPIRED": true, "RATE_LIMITED": true, "REVOKED": true, "ERROR": true,
	}
	if !validStatuses[body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid status"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"UPDATE credentials SET status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?",
		body.Status, now, now, credID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if body.LastError != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET last_error = ? WHERE id = ?", *body.LastError, credID); err != nil {
			h.logger.Error("update credential last_error", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.AccessToken != nil {
		enc, err := encryption.Encrypt(*body.AccessToken)
		if err != nil {
			h.logger.Error("encrypt access token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt token"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_value = ? WHERE id = ?", enc, credID); err != nil {
			h.logger.Error("update credential access token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.RefreshToken != nil {
		enc, err := encryption.Encrypt(*body.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt token"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_refresh_token = ? WHERE id = ?", enc, credID); err != nil {
			h.logger.Error("update credential refresh token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.TokenExpires != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET token_expires_at = ? WHERE id = ?", *body.TokenExpires, credID); err != nil {
			h.logger.Error("update credential token_expires_at", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": credID, "status": body.Status, "last_checked_at": now})
}

func (h *InternalHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID      string  `json:"chat_id"`
		AgentID     string  `json:"agent_id"`
		WorkspaceID string  `json:"workspace_id"`
		UserID      *string `json:"user_id"`
		Title       *string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.ChatID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat_id, agent_id, workspace_id required"})
		return
	}

	var existingID string
	if err := h.db.QueryRowContext(r.Context(), "SELECT id FROM chats WHERE id = ?", body.ChatID).Scan(&existingID); err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"id": existingID, "status": "already_exists"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'CHAT', 'ACTIVE', ?, ?)`,
		body.ChatID, body.AgentID, body.WorkspaceID, body.UserID, body.Title, now, now)
	if err != nil {
		h.logger.Error("create chat", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ChatID, "status": "created"})
}

func (h *InternalHandler) ResolveChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	var agentID, agentSlug, agentName, cliAdapter, toolProfile, wsID string
	var systemPrompt, roleTitle, agentRole sql.NullString
	var timeoutSecs int
	var memoryEnabled bool
	var crewID, crewSlug, crewName sql.NullString

	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.slug, a.name, a.role_title, a.agent_role, a.cli_adapter, a.system_prompt,
			a.tool_profile, a.timeout_seconds, a.memory_enabled,
			c2.id, c2.slug, c2.name, c.workspace_id
		FROM chats c
		JOIN agents a ON a.id = c.agent_id
		LEFT JOIN crews c2 ON c2.id = a.crew_id
		WHERE c.id = ?
	`, chatID).Scan(&agentID, &agentSlug, &agentName, &roleTitle, &agentRole, &cliAdapter, &systemPrompt,
		&toolProfile, &timeoutSecs, &memoryEnabled,
		&crewID, &crewSlug, &crewName, &wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat not found"})
			return
		}
		h.logger.Error("resolve chat", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.priority ASC
	`, agentID)
	if err != nil {
		h.logger.Error("resolve chat credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type credEntry struct {
		ID       string `json:"id"`
		EnvVar   string `json:"env_var"`
		Value    string `json:"value"`
		Priority int    `json:"priority"`
		Type     string `json:"type"`
	}

	var creds []credEntry
	for rows.Next() {
		var ce credEntry
		var encValue string
		if err := rows.Scan(&ce.ID, &ce.EnvVar, &ce.Priority, &encValue, &ce.Type); err != nil {
			h.logger.Error("scan credential for resolve", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		dec, err := encryption.Decrypt(encValue)
		if err != nil {
			h.logger.Error("decrypt credential for resolve", "id", ce.ID, "error", err)
			continue
		}
		ce.Value = dec
		creds = append(creds, ce)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (resolve credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if creds == nil {
		creds = []credEntry{}
	}

	crewIDStr := ""
	crewSlugStr := ""
	if crewID.Valid {
		crewIDStr = crewID.String
	}
	if crewSlug.Valid {
		crewSlugStr = crewSlug.String
	}

	// Build structured system prompt: ethos → identity → persona → skills
	// Note: crew context for LEADs is added later by the orchestrator
	var promptParts []string

	// Resolve agent_role (default to AGENT if unset)
	roleStr := "AGENT"
	if agentRole.Valid && agentRole.String != "" {
		roleStr = agentRole.String
	}

	// [CREWSHIP ETHOS] section — non-overridable, injected for every agent
	promptParts = append(promptParts, buildEthosBlock(roleStr))

	// [AGENT IDENTITY] section
	identityLines := []string{"[AGENT IDENTITY]"}
	identityLines = append(identityLines, fmt.Sprintf("Name: %s", agentName))
	if roleTitle.Valid && roleTitle.String != "" {
		identityLines = append(identityLines, fmt.Sprintf("Role: %s", roleTitle.String))
	}
	if crewName.Valid && crewName.String != "" {
		identityLines = append(identityLines, fmt.Sprintf("Crew: %s", crewName.String))
	}
	promptParts = append(promptParts, strings.Join(identityLines, "\n"))

	// [PERSONA] section -- user-defined system prompt
	if systemPrompt.Valid && systemPrompt.String != "" {
		promptParts = append(promptParts, "[PERSONA]\n"+systemPrompt.String)
	}

	// [SKILLS AVAILABLE] section
	const maxSkillsContextChars = 20000
	const skillHeader = "[SKILLS AVAILABLE]\nYou have access to the following skill playbooks. Activate them when the user's\nrequest matches each skill's \"When to Activate\" criteria.\n\n"
	const skillFooter = "\n[END SKILLS AVAILABLE]"
	const skillSeparator = "\n\n"
	// Budget for skill parts only — excludes header/footer overhead
	skillBudget := maxSkillsContextChars - len(skillHeader) - len(skillFooter)
	if skillBudget < 0 {
		skillBudget = 0
	}
	skillRows, err := h.db.QueryContext(r.Context(), `
		SELECT s.display_name, s.category, COALESCE(s.credential_requirements, '[]'), s.content
		FROM agent_skills as2
		JOIN skills s ON s.id = as2.skill_id
		WHERE as2.agent_id = ? AND as2.enabled = 1 AND s.content IS NOT NULL AND s.content != ''
		ORDER BY s.display_name
	`, agentID)
	if err != nil {
		h.logger.Error("resolve chat skills", "error", err)
	} else {
		defer skillRows.Close()

		// Build a set of configured env var names for credential status checks
		configuredEnvVars := make(map[string]bool, len(creds))
		for _, c := range creds {
			configuredEnvVars[c.EnvVar] = true
		}

		var skillParts []string
		totalSkillChars := 0
		for skillRows.Next() {
			var displayName, category, credReqJSON, content string
			if err := skillRows.Scan(&displayName, &category, &credReqJSON, &content); err != nil {
				h.logger.Error("scan skill for resolve", "error", err)
				continue
			}

			// Build credential status lines
			var credLines []string
			var credReqs []string
			if err := json.Unmarshal([]byte(credReqJSON), &credReqs); err == nil && len(credReqs) > 0 {
				for _, envVar := range credReqs {
					if configuredEnvVars[envVar] {
						credLines = append(credLines, fmt.Sprintf("  - %s: configured ✓", envVar))
					} else {
						credLines = append(credLines, fmt.Sprintf("  - %s: NOT CONFIGURED (skill may not work)", envVar))
					}
				}
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("<skill name=%q category=%q>\n", displayName, category))
			if len(credLines) > 0 {
				sb.WriteString("Credentials:\n")
				for _, cl := range credLines {
					sb.WriteString(cl + "\n")
				}
				sb.WriteString("\n")
			}
			sb.WriteString(content)
			sb.WriteString("\n</skill>")

			part := sb.String()
			sepLen := 0
			if len(skillParts) > 0 {
				sepLen = len(skillSeparator)
			}
			if totalSkillChars+sepLen+len(part) > skillBudget {
				// Truncate this skill to fit within budget
				remaining := skillBudget - totalSkillChars - sepLen
				suffix := "\n...(truncated)\n</skill>"
				if remaining > len(suffix)+20 {
					part = part[:remaining-len(suffix)] + suffix
					skillParts = append(skillParts, part)
					h.logger.Warn("skill truncated due to context budget", "skill", displayName, "budget", maxSkillsContextChars)
				} else {
					h.logger.Warn("skill omitted due to context budget", "skill", displayName, "budget", maxSkillsContextChars)
				}
				break
			}
			skillParts = append(skillParts, part)
			totalSkillChars += sepLen + len(part)
		}
		if err := skillRows.Err(); err != nil {
			h.logger.Error("rows iteration (resolve skills)", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if len(skillParts) > 0 {
			promptParts = append(promptParts, skillHeader+strings.Join(skillParts, skillSeparator)+skillFooter)
		}
	}

	// Query crew members for LEAD agents
	type crewMemberEntry struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		RoleTitle   string `json:"role_title"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	crewMembers := []crewMemberEntry{}
	if roleStr == "LEAD" && crewID.Valid {
		memberRows, err := h.db.QueryContext(r.Context(), `
			SELECT name, slug, COALESCE(role_title, ''), COALESCE(description, ''), status
			FROM agents
			WHERE crew_id = ? AND deleted_at IS NULL AND id != ?
			ORDER BY name
		`, crewID.String, agentID)
		if err != nil {
			h.logger.Error("query crew members for lead", "error", err)
		} else {
			defer memberRows.Close()
			for memberRows.Next() {
				var m crewMemberEntry
				if err := memberRows.Scan(&m.Name, &m.Slug, &m.RoleTitle, &m.Description, &m.Status); err != nil {
					h.logger.Error("scan crew member", "error", err)
					continue
				}
				crewMembers = append(crewMembers, m)
			}
			if err := memberRows.Err(); err != nil {
				h.logger.Error("rows iteration (crew members)", "error", err)
			}
		}
	}

	sysPrompt := strings.Join(promptParts, "\n\n")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id":        agentID,
		"agent_slug":      agentSlug,
		"agent_role":      roleStr,
		"crew_id":         crewIDStr,
		"crew_slug":       crewSlugStr,
		"container_id":    "",
		"cli_adapter":     cliAdapter,
		"system_prompt":   sysPrompt,
		"tool_profile":    toolProfile,
		"credentials":     creds,
		"timeout_seconds": timeoutSecs,
		"workspace_id":    wsID,
		"memory_enabled":  memoryEnabled,
		"crew_members":    crewMembers,
	})
}

func (h *InternalHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string          `json:"id"`
		AgentID     string          `json:"agent_id"`
		ChatID      string          `json:"chat_id"`
		WorkspaceID string          `json:"workspace_id"`
		TriggerType string          `json:"trigger_type"`
		Metadata    json.RawMessage `json:"metadata"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.ID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id, agent_id, workspace_id required"})
		return
	}
	if body.TriggerType == "" {
		body.TriggerType = "USER"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var metadataVal interface{}
	if body.Metadata != nil {
		metadataVal = string(body.Metadata)
	}
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agent_runs (id, agent_id, chat_id, workspace_id, trigger_type, status, metadata, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'RUNNING', ?, ?, ?)`,
		body.ID, body.AgentID, body.ChatID, body.WorkspaceID, body.TriggerType, metadataVal, now, now)
	if err != nil {
		h.logger.Error("create run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ID, "status": "RUNNING"})
}

func (h *InternalHandler) UpdateRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	var body struct {
		Status       string          `json:"status"`
		ExitCode     *int            `json:"exit_code"`
		ErrorMessage *string         `json:"error_message"`
		Metadata     json.RawMessage `json:"metadata"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	validStatuses := map[string]bool{
		"RUNNING": true, "COMPLETED": true, "FAILED": true, "CANCELLED": true,
	}
	if !validStatuses[body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid status"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	terminal := map[string]bool{"COMPLETED": true, "FAILED": true, "CANCELLED": true}
	query := "UPDATE agent_runs SET status = ?"
	args := []interface{}{body.Status}
	if terminal[body.Status] {
		query += ", finished_at = ?"
		args = append(args, now)
	}

	if body.ExitCode != nil {
		query += ", exit_code = ?"
		args = append(args, *body.ExitCode)
	}
	if body.ErrorMessage != nil {
		query += ", error_message = ?"
		args = append(args, *body.ErrorMessage)
	}
	if body.Metadata != nil {
		query += ", metadata = ?"
		args = append(args, string(body.Metadata))
	}
	query += " WHERE id = ?"
	args = append(args, runID)

	_, err := h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("update run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": runID, "status": body.Status})
}

// buildEthosBlock returns the [CREWSHIP ETHOS] system prompt block.
// This block is non-overridable and injected for every agent, with role-specific variations.
func buildEthosBlock(agentRole string) string {
	var roleText string
	switch agentRole {
	case "LEAD":
		roleText = `You are a crew member with orchestration responsibility on the Crewship -- ` +
			`an expedition with a shared purpose. You are not a boss -- you are an equal ` +
			`colleague who carries the soul and mission of the expedition to the whole team, ` +
			`and that is how the ship sails towards adventure. Your crew trusts you because ` +
			`you are one of them, just with a different task.`
	case "COORDINATOR":
		roleText = `You are a workspace member with coordination responsibility on the Crewship -- ` +
			`connecting the expeditions of all crews towards one shared goal. You are not above ` +
			`anyone -- you are an equal who sees the bigger picture and helps crews align ` +
			`their efforts towards the common adventure.`
	default: // AGENT
		roleText = `You are part of a crew on the Crewship -- an expedition with a shared purpose ` +
			`that transcends any individual. Your work matters because it contributes to ` +
			`something greater than yourself.`
	}
	return "[CREWSHIP ETHOS]\n" + roleText
}

func WriteAuditLog(ctx context.Context, db *sql.DB, action, entityType, entityID, userID, workspaceID string, metadata map[string]interface{}) {
	now := time.Now().UTC().Format(time.RFC3339)
	metaJSON := "{}"
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			metaJSON = string(b)
		}
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, userID, action, entityType, entityID, metaJSON, now)
	if err != nil {
		slog.Debug("audit log write failed", "error", err, "action", action)
	}
}
