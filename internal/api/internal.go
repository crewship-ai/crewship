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
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/ws"
)

type mcpCredEntry struct {
	ID       string `json:"id"`
	EnvVar   string `json:"env_var"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

type InternalHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	internalToken  string
	keeperEnabled  atomic.Bool
	hub            *ws.Hub
}

func NewInternalHandler(db *sql.DB, internalToken string, logger *slog.Logger) *InternalHandler {
	return &InternalHandler{db: db, internalToken: internalToken, logger: logger}
}

func (h *InternalHandler) SetHub(hub *ws.Hub) {
	h.hub = hub
}

func (h *InternalHandler) SetKeeperEnabled(enabled bool) {
	h.keeperEnabled.Store(enabled)
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
		WHERE status IN ('ACTIVE', 'EXPIRED', 'ERROR') AND deleted_at IS NULL
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

	var agentID string
	err := h.db.QueryRowContext(r.Context(), "SELECT agent_id FROM chats WHERE id = ?", chatID).Scan(&agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat not found"})
			return
		}
		h.logger.Error("resolve chat lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.resolveAgentConfig(w, r, agentID)
}

func (h *InternalHandler) ResolveAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	h.resolveAgentConfig(w, r, agentID)
}

func (h *InternalHandler) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	var secret sql.NullString
	err := h.db.QueryRowContext(r.Context(), "SELECT webhook_secret FROM agents WHERE id = ?", agentID).Scan(&secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("webhook secret lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret.String})
}

func (h *InternalHandler) resolveAgentConfig(w http.ResponseWriter, r *http.Request, agentID string) {
	var agentSlug, agentName, cliAdapter, toolProfile, wsID string
	var systemPrompt, roleTitle, agentRole, llmModel sql.NullString
	var timeoutSecs int
	var memoryEnabled bool
	var crewID, crewSlug, crewName sql.NullString
	var crewNetworkMode, crewAllowedDomains sql.NullString
	var crewMemoryMB, crewTTLHours sql.NullInt64
	var crewCPUs sql.NullFloat64
	var crewMCPConfigJSON, agentMCPConfigJSON sql.NullString

	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.slug, a.name, a.role_title, a.agent_role, a.cli_adapter, a.system_prompt,
			a.tool_profile, a.timeout_seconds, a.memory_enabled,
			c2.id, c2.slug, c2.name, a.workspace_id, a.llm_model,
			c2.network_mode, c2.allowed_domains,
			c2.container_memory_mb, c2.container_cpus, c2.container_ttl_hours,
			c2.mcp_config_json, a.mcp_config_json
		FROM agents a
		LEFT JOIN crews c2 ON c2.id = a.crew_id
		WHERE a.id = ?
	`, agentID).Scan(&agentSlug, &agentName, &roleTitle, &agentRole, &cliAdapter, &systemPrompt,
		&toolProfile, &timeoutSecs, &memoryEnabled,
		&crewID, &crewSlug, &crewName, &wsID, &llmModel,
		&crewNetworkMode, &crewAllowedDomains,
		&crewMemoryMB, &crewCPUs, &crewTTLHours,
		&crewMCPConfigJSON, &agentMCPConfigJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("resolve agent config", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Auto-migrate crew JSON blob to integration tables if present.
	if crewMCPConfigJSON.Valid && crewMCPConfigJSON.String != "" && crewID.Valid {
		if err := MigrateJSONBlobToCrewServers(r.Context(), h.db, h.logger, crewID.String, wsID, crewMCPConfigJSON.String); err != nil {
			h.logger.Warn("auto-migrate crew MCP config in resolveAgentConfig", "crew_id", crewID.String, "error", err)
		} else {
			crewMCPConfigJSON.String = ""
			crewMCPConfigJSON.Valid = false
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.priority ASC
	`, agentID)
	if err != nil {
		h.logger.Error("resolve agent credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var creds []mcpCredEntry
	for rows.Next() {
		var ce mcpCredEntry
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
		creds = []mcpCredEntry{}
	}

	// Auto-resolve credentials referenced in crew/agent MCP configs.
	// The MCP editor stores env vars as ${GOOGLE_ACCESS_TOKEN} and creates
	// credentials named "google-access-token-oauth-<suffix>".  Match by
	// converting the env var name to the derived prefix (lowercase, hyphens)
	// and finding the most-recently created workspace credential whose name
	// starts with that prefix.
	creds = autoResolveMCPCredentials(r.Context(), h.db, h.logger, wsID, creds,
		crewMCPConfigJSON.String, agentMCPConfigJSON.String)

	crewIDStr := ""
	crewSlugStr := ""
	if crewID.Valid {
		crewIDStr = crewID.String
	}
	if crewSlug.Valid {
		crewSlugStr = crewSlug.String
	}

	// Query workspace preferred_language
	var preferredLanguage sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT preferred_language FROM workspaces WHERE id = ?", wsID).Scan(&preferredLanguage); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		h.logger.Warn("preferred language lookup failed", "error", err)
	}

	// Build structured system prompt: ethos → language → identity → persona → skills
	// Note: crew context for LEADs is added later by the orchestrator
	var promptParts []string

	// Resolve agent_role (default to AGENT if unset)
	roleStr := "AGENT"
	if agentRole.Valid && agentRole.String != "" {
		roleStr = agentRole.String
	}

	// [CREWSHIP ETHOS] section — non-overridable, injected for every agent
	promptParts = append(promptParts, buildEthosBlock(roleStr))

	// [LANGUAGE PREFERENCE] section — injected when workspace has a preferred language
	if preferredLanguage.Valid && preferredLanguage.String != "" {
		lang := preferredLanguage.String
		promptParts = append(promptParts, fmt.Sprintf(
			"[LANGUAGE PREFERENCE]\nAlways respond in: %s\nAll output, thinking, and communication must be in %s.\nIf the user writes in a different language, still respond in %s unless explicitly asked otherwise.\n[END LANGUAGE PREFERENCE]",
			lang, lang, lang))
	}

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
		h.logger.Error("resolve agent skills", "error", err)
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

	// Query crew members for all agents in a crew (enables peer communication)
	type memberIntegrationEntry struct {
		Name       string   `json:"name"`
		ServerName string   `json:"server_name"`
		Tools      []string `json:"tools"`
	}
	type crewMemberEntry struct {
		ID           string                    `json:"id"`
		Name         string                    `json:"name"`
		Slug         string                    `json:"slug"`
		RoleTitle    string                    `json:"role_title"`
		Description  string                    `json:"description"`
		Status       string                    `json:"status"`
		ChatID       string                    `json:"chat_id,omitempty"`
		Integrations []memberIntegrationEntry  `json:"integrations,omitempty"`
	}
	crewMembers := []crewMemberEntry{}
	if crewID.Valid {
		memberRows, err := h.db.QueryContext(r.Context(), `
			SELECT a.id, a.name, a.slug, COALESCE(a.role_title, ''), COALESCE(a.description, ''), a.status,
			       COALESCE((SELECT c.id FROM chats c WHERE c.agent_id = a.id AND c.status = 'ACTIVE' ORDER BY c.created_at DESC LIMIT 1), '')
			FROM agents a
			WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
			ORDER BY a.name
		`, crewID.String, agentID)
		if err != nil {
			h.logger.Error("query crew members for crew", "error", err)
		} else {
			defer memberRows.Close()
			for memberRows.Next() {
				var m crewMemberEntry
				if err := memberRows.Scan(&m.ID, &m.Name, &m.Slug, &m.RoleTitle, &m.Description, &m.Status, &m.ChatID); err != nil {
					h.logger.Error("scan crew member", "error", err)
					continue
				}
				crewMembers = append(crewMembers, m)
			}
			if err := memberRows.Err(); err != nil {
				h.logger.Error("rows iteration (crew members)", "error", err)
			}
		}
		// Enrich crew members with MCP integration info (single batch query)
		if (roleStr == "LEAD" || roleStr == "COORDINATOR") && len(crewMembers) > 0 {
			memberIdx := make(map[string]int, len(crewMembers))
			placeholders := make([]string, len(crewMembers))
			args := make([]interface{}, len(crewMembers))
			for i, m := range crewMembers {
				memberIdx[m.ID] = i
				placeholders[i] = "?"
				args[i] = m.ID
			}
			if igRows, err := h.db.QueryContext(r.Context(), `
				SELECT b.agent_id,
					COALESCE(CASE b.mcp_server_scope
						WHEN 'workspace' THEN ws.display_name
						WHEN 'crew' THEN cs.display_name END, ''),
					COALESCE(CASE b.mcp_server_scope
						WHEN 'workspace' THEN ws.name
						WHEN 'crew' THEN cs.name END, '')
				FROM agent_mcp_bindings b
				LEFT JOIN workspace_mcp_servers ws ON b.mcp_server_id = ws.id AND b.mcp_server_scope = 'workspace' AND ws.deleted_at IS NULL
				LEFT JOIN crew_mcp_servers cs ON b.mcp_server_id = cs.id AND b.mcp_server_scope = 'crew' AND cs.deleted_at IS NULL
				WHERE b.agent_id IN (`+strings.Join(placeholders, ",")+`) AND b.enabled = 1`,
				args...); err == nil {
				for igRows.Next() {
					var aid, displayName, serverName string
					if igRows.Scan(&aid, &displayName, &serverName) == nil && serverName != "" {
						if idx, ok := memberIdx[aid]; ok {
							crewMembers[idx].Integrations = append(crewMembers[idx].Integrations,
								memberIntegrationEntry{Name: displayName, ServerName: serverName})
						}
					}
				}
				igRows.Close()
			}
		}
	}

	// For COORDINATOR agents, load all workspace crews and their agents
	type crewInfoEntry struct {
		ID      string           `json:"id"`
		Name    string           `json:"name"`
		Slug    string           `json:"slug"`
		Members []crewMemberEntry `json:"members"`
	}
	var allCrews []crewInfoEntry
	if roleStr == "COORDINATOR" {
		crewRows, err := h.db.QueryContext(r.Context(), `
			SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`,
			wsID)
		if err != nil {
			h.logger.Error("query crews for coordinator", "error", err)
		} else {
			defer crewRows.Close()
			for crewRows.Next() {
				var ci crewInfoEntry
				if err := crewRows.Scan(&ci.ID, &ci.Name, &ci.Slug); err != nil {
					h.logger.Error("scan crew for coordinator", "error", err)
					continue
				}
				agentRows, err := h.db.QueryContext(r.Context(), `
					SELECT a.id, a.name, a.slug, COALESCE(a.role_title, ''), COALESCE(a.description, ''), a.status,
					       COALESCE((SELECT c.id FROM chats c WHERE c.agent_id = a.id AND c.status = 'ACTIVE' ORDER BY c.created_at DESC LIMIT 1), '')
					FROM agents a
					WHERE a.crew_id = ? AND a.deleted_at IS NULL
					ORDER BY a.name`, ci.ID)
				if err != nil {
					h.logger.Error("query agents for coordinator crew", "error", err, "crew_id", ci.ID)
				} else {
					for agentRows.Next() {
						var m crewMemberEntry
						if err := agentRows.Scan(&m.ID, &m.Name, &m.Slug, &m.RoleTitle, &m.Description, &m.Status, &m.ChatID); err != nil {
							h.logger.Error("scan agent for coordinator", "error", err)
							continue
						}
						ci.Members = append(ci.Members, m)
					}
					agentRows.Close()
				}
				allCrews = append(allCrews, ci)
			}
		}
	}

	// [KEEPER] section — credential access control instructions
	if h.keeperEnabled.Load() {
		// Collect SECRET credentials for this agent and redact their values
		var secretCreds []string
		for i := range creds {
			if creds[i].Type == "SECRET" {
				secretCreds = append(secretCreds, creds[i].EnvVar)
				creds[i].Value = ""
			}
		}
		if len(secretCreds) > 0 {
			var keeperBlock strings.Builder
			keeperBlock.WriteString("[CREDENTIAL ACCESS CONTROL — KEEPER]\n")
			keeperBlock.WriteString("Some credentials require explicit approval before use.\n")
			keeperBlock.WriteString("You do NOT have these credentials in your environment. To access them:\n\n")
			keeperBlock.WriteString("  curl -s -X POST http://localhost:9119/keeper/request \\\n")
			keeperBlock.WriteString("    -H \"Content-Type: application/json\" \\\n")
			keeperBlock.WriteString(fmt.Sprintf("    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why you need it>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug))
			keeperBlock.WriteString("The Keeper (AI gatekeeper) will evaluate your request and respond with ALLOW or DENY.\n")
			keeperBlock.WriteString("If ALLOW, the response contains the credential value. If DENY, do NOT retry — explain to the user why access was denied.\n\n")
			keeperBlock.WriteString("To execute a command with a credential (without seeing the value):\n")
			keeperBlock.WriteString("  curl -s -X POST http://localhost:9119/keeper/execute \\\n")
			keeperBlock.WriteString("    -H \"Content-Type: application/json\" \\\n")
			keeperBlock.WriteString(fmt.Sprintf("    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why>\",\"command\":\"<shell command>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug))
			keeperBlock.WriteString("Keeper-guarded credentials available to you:\n")
			for _, name := range secretCreds {
				keeperBlock.WriteString(fmt.Sprintf("  - %s\n", name))
			}
			keeperBlock.WriteString("[END CREDENTIAL ACCESS CONTROL]")
			promptParts = append(promptParts, keeperBlock.String())
		}
	}

	sysPrompt := strings.Join(promptParts, "\n\n")

	llmModelStr := ""
	if llmModel.Valid {
		llmModelStr = llmModel.String
	}

	networkMode := "free"
	if crewNetworkMode.Valid && crewNetworkMode.String != "" {
		mode := crewNetworkMode.String
		if mode == "free" || mode == "restricted" {
			networkMode = mode
		} else {
			// Unknown mode in DB — fail closed to prevent silent egress
			h.logger.Error("unknown network_mode in DB, defaulting to restricted", "mode", mode, "crew_id", crewIDStr)
			networkMode = "restricted"
		}
	}
	allowedDomains := []string{}
	if crewAllowedDomains.Valid && crewAllowedDomains.String != "" {
		if err := json.Unmarshal([]byte(crewAllowedDomains.String), &allowedDomains); err != nil {
			h.logger.Error("malformed allowed_domains JSON in DB, defaulting to empty", "error", err, "crew_id", crewIDStr)
			allowedDomains = []string{}
		}
	}

	memoryMB := 4096
	if crewMemoryMB.Valid {
		memoryMB = int(crewMemoryMB.Int64)
	}
	cpus := 2.0
	if crewCPUs.Valid {
		cpus = crewCPUs.Float64
	}
	ttlHours := 0
	if crewTTLHours.Valid {
		ttlHours = int(crewTTLHours.Int64)
	}

	// Resolve MCP integrations for this agent (workspace → crew → agent cascade)
	type mcpServerEntry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Transport   string `json:"transport"`
		Endpoint    *string `json:"endpoint,omitempty"`
		Command     *string `json:"command,omitempty"`
		Args        []string `json:"args,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
		CredToken    string `json:"cred_token,omitempty"`
		CredType     string `json:"cred_type,omitempty"`
		CredHeader   string `json:"cred_header,omitempty"`
		EnvVarName   string `json:"env_var_name,omitempty"`
	}
	type mcpServerRow struct {
		id, name, displayName, transport string
		endpoint, command, argsJSON, envJSON *string
	}
	var mcpServers []mcpServerEntry
	{
		// Step 1: Workspace MCP servers (keyed by name)
		merged := make(map[string]*mcpServerRow)
		if wsRows, err := h.db.QueryContext(r.Context(), `
			SELECT id, name, display_name, transport, endpoint, command, args_json, env_json
			FROM workspace_mcp_servers WHERE workspace_id = ? AND enabled = 1 AND deleted_at IS NULL`, wsID); err == nil {
			for wsRows.Next() {
				var s mcpServerRow
				if err := wsRows.Scan(&s.id, &s.name, &s.displayName, &s.transport, &s.endpoint, &s.command, &s.argsJSON, &s.envJSON); err != nil {
					continue
				}
				merged[s.name] = &s
			}
			wsRows.Close()
		}

		// Step 2: Crew MCP servers override workspace by name
		if crewID.Valid {
			if crewRows, err := h.db.QueryContext(r.Context(), `
				SELECT cs.id, cs.name, cs.display_name, cs.transport, cs.endpoint, cs.command, cs.args_json, cs.env_json
				FROM crew_mcp_servers cs
				JOIN crews c ON c.id = cs.crew_id AND c.deleted_at IS NULL
				WHERE cs.crew_id = ? AND cs.enabled = 1 AND cs.deleted_at IS NULL`, crewID.String); err == nil {
				for crewRows.Next() {
					var s mcpServerRow
					if err := crewRows.Scan(&s.id, &s.name, &s.displayName, &s.transport, &s.endpoint, &s.command, &s.argsJSON, &s.envJSON); err != nil {
						continue
					}
					merged[s.name] = &s
				}
				crewRows.Close()
			}
		}

		// Step 3: Agent bindings (opt-out + credential assignment)
		type bindInfo struct {
			credID     *string
			credType   string
			credHeader string
			envVarName string
			enabled    bool
		}
		agentBindings := make(map[string]*bindInfo)
		if bindRows, err := h.db.QueryContext(r.Context(), `
			SELECT mcp_server_id, credential_id, enabled, COALESCE(cred_type, ''), COALESCE(cred_header, ''), COALESCE(env_var_name, '')
			FROM agent_mcp_bindings WHERE agent_id = ?`, agentID); err == nil {
			for bindRows.Next() {
				var sid, ct, ch, evn string
				var credID *string
				var enabled int
				if err := bindRows.Scan(&sid, &credID, &enabled, &ct, &ch, &evn); err != nil {
					continue
				}
				agentBindings[sid] = &bindInfo{credID: credID, enabled: enabled == 1, credType: ct, credHeader: ch, envVarName: evn}
			}
			bindRows.Close()
		}

		// Step 4: Batch credential lookup (avoid N+1)
		credIDs := make([]string, 0)
		for _, srv := range merged {
			if b, ok := agentBindings[srv.id]; ok && b.enabled && b.credID != nil && *b.credID != "" {
				credIDs = append(credIDs, *b.credID)
			}
		}
		credTokens := make(map[string]string) // credID → plaintext
		if len(credIDs) > 0 {
			placeholders := make([]string, len(credIDs))
			args := make([]interface{}, len(credIDs))
			for i, id := range credIDs {
				placeholders[i] = "?"
				args[i] = id
			}
			if credRows, err := h.db.QueryContext(r.Context(),
				"SELECT id, encrypted_value FROM credentials WHERE id IN ("+strings.Join(placeholders, ",")+") AND deleted_at IS NULL",
				args...); err == nil {
				for credRows.Next() {
					var cid, encVal string
					if credRows.Scan(&cid, &encVal) == nil {
						if plain, err := encryption.Decrypt(encVal); err == nil {
							credTokens[cid] = plain
						}
					}
				}
				credRows.Close()
			}
		}

		// Step 4b: Check which servers have ANY bindings (for opt-in filtering).
		// If a server has at least one binding, only agents WITH a binding see it.
		serversWithBindings := make(map[string]bool)
		if bindCountRows, err := h.db.QueryContext(r.Context(), `
			SELECT mcp_server_id FROM agent_mcp_bindings
			GROUP BY mcp_server_id HAVING COUNT(*) > 0`); err == nil {
			for bindCountRows.Next() {
				var sid string
				if bindCountRows.Scan(&sid) == nil {
					serversWithBindings[sid] = true
				}
			}
			bindCountRows.Close()
		}

		// Step 5: Build result entries
		for _, srv := range merged {
			entry := mcpServerEntry{
				ID: srv.id, Name: srv.name, DisplayName: srv.displayName,
				Transport: srv.transport, Endpoint: srv.endpoint, Command: srv.command,
			}
			// Parse args_json
			if srv.argsJSON != nil && *srv.argsJSON != "" {
				if err := json.Unmarshal([]byte(*srv.argsJSON), &entry.Args); err != nil {
					h.logger.Warn("malformed args_json for MCP server", "server_id", srv.id, "error", err)
				}
			}
			// Parse env_json
			if srv.envJSON != nil && *srv.envJSON != "" {
				if err := json.Unmarshal([]byte(*srv.envJSON), &entry.Env); err != nil {
					h.logger.Warn("malformed env_json for MCP server", "server_id", srv.id, "error", err)
				}
			}
			b, hasBind := agentBindings[srv.id]
			if hasBind {
				if !b.enabled {
					continue // agent opted out
				}
				if srv.transport == "stdio" {
					entry.EnvVarName = b.envVarName
				}
				if b.credID != nil && *b.credID != "" {
					if token, ok := credTokens[*b.credID]; ok {
						entry.CredToken = token
						entry.CredType = b.credType
						entry.CredHeader = b.credHeader
						if entry.CredType == "" {
							entry.CredType = "bearer"
						}
					}
				}
			} else if serversWithBindings[srv.id] {
				// Server has bindings for other agents but NOT this one → skip
				continue
			}
			mcpServers = append(mcpServers, entry)
		}
	}

	// Auto-resolve credentials from table-based MCP servers' env_json.
	// This covers the case where MCP config was migrated from JSON blob
	// to crew_mcp_servers table — the env_json contains ${VAR} references
	// that need credential resolution.
	if len(mcpServers) > 0 {
		var envJsons []string
		for _, srv := range mcpServers {
			if len(srv.Env) > 0 {
				if b, err := json.Marshal(map[string]interface{}{"mcpServers": map[string]interface{}{srv.Name: map[string]interface{}{"env": srv.Env}}}); err == nil {
					envJsons = append(envJsons, string(b))
				}
			}
		}
		if len(envJsons) > 0 {
			creds = autoResolveMCPCredentials(r.Context(), h.db, h.logger, wsID, creds, envJsons...)
		}
	}

	// For OAUTH2 credentials that were auto-resolved (client_id/secret),
	// also include the access token so the orchestrator can write tokens.json.
	// Use a synthetic env var "_OAUTH_ACCESS_TOKEN:<credID>" so the orchestrator
	// can find it without an actual env var reference.
	{
		resolvedOAuthIDs := make(map[string]bool)
		for _, c := range creds {
			if c.Type == "OAUTH2" {
				resolvedOAuthIDs[c.ID] = true
			}
		}
		for credID := range resolvedOAuthIDs {
			// Check if access token is already in creds
			hasAccessToken := false
			for _, c := range creds {
				if c.ID == credID && !strings.HasSuffix(c.EnvVar, "_CLIENT_ID") && !strings.HasSuffix(c.EnvVar, "_CLIENT_SECRET") {
					hasAccessToken = true
					break
				}
			}
			if hasAccessToken {
				continue
			}
			// Fetch and decrypt the access token
			var encVal string
			if err := h.db.QueryRowContext(r.Context(),
				"SELECT encrypted_value FROM credentials WHERE id = ? AND deleted_at IS NULL", credID).Scan(&encVal); err == nil {
				if dec, err := encryption.Decrypt(encVal); err == nil && dec != "" {
					creds = append(creds, mcpCredEntry{
						ID:     credID,
						EnvVar: "_OAUTH_ACCESS_TOKEN:" + credID,
						Value:  dec,
						Type:   "OAUTH2",
					})
				}
			}
		}
	}

	resp := map[string]interface{}{
		"agent_id":        agentID,
		"agent_slug":      agentSlug,
		"agent_role":      roleStr,
		"crew_id":         crewIDStr,
		"crew_slug":       crewSlugStr,
		"container_id":    "",
		"cli_adapter":     cliAdapter,
		"llm_model":       llmModelStr,
		"system_prompt":   sysPrompt,
		"tool_profile":    toolProfile,
		"credentials":     creds,
		"timeout_seconds": timeoutSecs,
		"workspace_id":    wsID,
		"memory_enabled":  memoryEnabled,
		"crew_members":    crewMembers,
		"network_mode":    networkMode,
		"allowed_domains": allowedDomains,
		"memory_mb":       memoryMB,
		"cpus":            cpus,
		"ttl_hours":       ttlHours,
		"mcp_servers":          mcpServers,
		"crew_mcp_config_json":  crewMCPConfigJSON.String,
		"agent_mcp_config_json": agentMCPConfigJSON.String,
	}
	if len(allCrews) > 0 {
		resp["all_crews"] = allCrews

		// Load active missions for COORDINATOR context
		missionRows, err := h.db.QueryContext(r.Context(), `
			SELECT m.id, c.slug, m.title, m.status
			FROM missions m
			JOIN crews c ON c.id = m.crew_id
			WHERE m.workspace_id = ? AND m.status IN ('PLANNING', 'IN_PROGRESS', 'REVIEW')
			ORDER BY m.created_at DESC LIMIT 20`,
			wsID)
		if err == nil {
			defer missionRows.Close()
			type missionEntry struct {
				ID       string `json:"id"`
				CrewSlug string `json:"crew_slug"`
				Title    string `json:"title"`
				Status   string `json:"status"`
			}
			var activeMissions []missionEntry
			for missionRows.Next() {
				var me missionEntry
				if err := missionRows.Scan(&me.ID, &me.CrewSlug, &me.Title, &me.Status); err == nil {
					activeMissions = append(activeMissions, me)
				}
			}
			if len(activeMissions) > 0 {
				resp["active_missions"] = activeMissions
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
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

	// Update agent status to RUNNING
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'RUNNING', updated_at = ? WHERE id = ?", now, body.AgentID); err != nil {
		h.logger.Debug("update agent status on run create", "error", err, "agent_id", body.AgentID)
	}

	// Broadcast real-time events
	if h.hub != nil {
		var agentName string
		if err := h.db.QueryRowContext(r.Context(), "SELECT name FROM agents WHERE id = ?", body.AgentID).Scan(&agentName); err != nil {
			h.logger.Debug("fetch agent name for broadcast", "error", err, "agent_id", body.AgentID)
		}

		channel := "workspace:" + body.WorkspaceID
		h.hub.Broadcast(channel, ws.ServerMessage{
			Type:    "run.started",
			Channel: channel,
			Payload: map[string]string{
				"run_id":    body.ID,
				"agent_id":  body.AgentID,
				"agent_name": agentName,
				"status":    "RUNNING",
			},
		})
		h.hub.Broadcast(channel, ws.ServerMessage{
			Type:    "agent.status",
			Channel: channel,
			Payload: map[string]string{
				"agent_id":  body.AgentID,
				"agent_name": agentName,
				"status":    "RUNNING",
			},
		})
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

	// Update agent status and broadcast events for terminal states
	if terminal[body.Status] {
		var agentID, workspaceID string
		var agentName sql.NullString
		if err := h.db.QueryRowContext(r.Context(),
			`SELECT r.agent_id, r.workspace_id, a.name FROM agent_runs r
			 LEFT JOIN agents a ON a.id = r.agent_id WHERE r.id = ?`, runID,
		).Scan(&agentID, &workspaceID, &agentName); err != nil {
			h.logger.Debug("fetch run details for broadcast", "error", err, "run_id", runID)
		}

		// Atomic agent status update: always runs regardless of hub presence
		agentStatus := "IDLE"
		if agentID != "" {
			failedStatus := "IDLE"
			if body.Status == "FAILED" {
				failedStatus = "ERROR"
			}
			if _, err := h.db.ExecContext(r.Context(), `
				UPDATE agents SET status = CASE
					WHEN (SELECT COUNT(*) FROM agent_runs WHERE agent_id = ? AND status = 'RUNNING' AND id != ?) > 0 THEN 'RUNNING'
					ELSE ?
				END, updated_at = ? WHERE id = ?`,
				agentID, runID, failedStatus, now, agentID); err != nil {
				h.logger.Debug("update agent status on run completion", "error", err, "agent_id", agentID)
			}

			// Read back actual status
			agentStatus = failedStatus
			var readBack string
			if err := h.db.QueryRowContext(r.Context(), "SELECT status FROM agents WHERE id = ?", agentID).Scan(&readBack); err == nil {
				agentStatus = readBack
			}
		}

		// Broadcast real-time events (only when hub is available)
		if h.hub != nil && workspaceID != "" {
			channel := "workspace:" + workspaceID
			eventType := "run.completed"
			if body.Status == "FAILED" || body.Status == "CANCELLED" {
				eventType = "run.failed"
			}
			h.hub.Broadcast(channel, ws.ServerMessage{
				Type: eventType, Channel: channel, Payload: map[string]string{
					"run_id":     runID,
					"agent_id":   agentID,
					"agent_name": agentName.String,
					"status":     body.Status,
				},
			})
			h.hub.Broadcast(channel, ws.ServerMessage{
				Type: "agent.status", Channel: channel, Payload: map[string]string{
					"agent_id":   agentID,
					"agent_name": agentName.String,
					"status":     agentStatus,
				},
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": runID, "status": body.Status})
}

func (h *InternalHandler) IncrementMessageCount(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Delta int `json:"delta"`
	}
	if err := readJSON(r, &body); err != nil || body.Delta <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid delta"})
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		"UPDATE chats SET message_count = message_count + ? WHERE id = ?",
		body.Delta, chatID)
	if err != nil {
		h.logger.Error("increment message count", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID})
}

func (h *InternalHandler) UpdateChatTitle(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Title string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil || body.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title required"})
		return
	}
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE chats SET title = ? WHERE id = ? AND (title IS NULL OR title = '')",
		body.Title, chatID)
	if err != nil {
		h.logger.Error("update chat title", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat not found or already titled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID, "title": body.Title})
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

// ListCrews handles GET /api/v1/internal/crews?workspace_id=...
// Used by the sidecar on behalf of COORDINATOR agents.
func (h *InternalHandler) ListCrews(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	type crewEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`, wsID)
	if err != nil {
		h.logger.Error("list crews internal", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	result := []crewEntry{}
	for rows.Next() {
		var c crewEntry
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateCrew handles POST /api/v1/internal/crews?workspace_id=...
// Allows COORDINATOR agents (via sidecar) to create a new crew in the workspace.
func (h *InternalHandler) CreateCrew(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	var body struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
		Color       string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required (could not derive from name)"})
		return
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check crew slug uniqueness", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if existing > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("crew with slug '%s' already exists", body.Slug)})
		return
	}

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	var icon, color *string
	if body.Icon != "" {
		icon = &body.Icon
	}
	if body.Color != "" {
		color = &body.Color
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, description, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, body.Name, body.Slug, body.Description, icon, color, now, now)
	if err != nil {
		h.logger.Error("internal create crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create crew"})
		return
	}

	h.logger.Info("crew created via coordinator", "crew_id", crewID, "name", body.Name, "workspace", wsID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":          crewID,
		"name":        body.Name,
		"slug":        body.Slug,
		"workspace_id": wsID,
	})
}

// CreateAgent handles POST /api/v1/internal/agents?workspace_id=...
// Allows COORDINATOR agents (via sidecar) to create a new agent within a crew.
func (h *InternalHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	var body struct {
		CrewID       string `json:"crew_id"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		RoleTitle    string `json:"role_title"`
		AgentRole    string `json:"agent_role"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		CLIAdapter   string `json:"cli_adapter"`
		LLMProvider  string `json:"llm_provider"`
		LLMModel     string `json:"llm_model"`
		ToolProfile  string `json:"tool_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Name == "" || body.CrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and crew_id are required"})
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required (could not derive from name)"})
		return
	}
	if body.AgentRole == "" {
		body.AgentRole = "AGENT"
	}
	if body.CLIAdapter == "" {
		body.CLIAdapter = "CLAUDE_CODE"
	}
	if body.ToolProfile == "" {
		body.ToolProfile = "CODING"
	}

	// Suffix slug with crew slug to prevent workspace-wide UNIQUE conflicts
	var crewSlug string
	if err := h.db.QueryRowContext(r.Context(), `SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, body.CrewID, wsID).Scan(&crewSlug); err != nil {
		h.logger.Warn("lookup crew slug", "crew_id", body.CrewID, "error", err)
	}
	if crewSlug != "" {
		body.Slug = body.Slug + "-" + crewSlug
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM agents WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check agent slug uniqueness", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if existing > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("agent with slug '%s' already exists", body.Slug)})
		return
	}

	agentID := generateCUID()
	webhookSecret := generateWebhookSecret()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, description, role_title, agent_role,
			cli_adapter, llm_provider, llm_model, tool_profile, system_prompt,
			timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, wsID, body.CrewID, body.Name, body.Slug, body.Description,
		body.RoleTitle, body.AgentRole,
		body.CLIAdapter, nilIfEmpty(body.LLMProvider), nilIfEmpty(body.LLMModel), body.ToolProfile, body.SystemPrompt,
		1800, true, webhookSecret, now, now)
	if err != nil {
		h.logger.Error("internal create agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create agent"})
		return
	}

	// Auto-assign workspace AI credentials so the new agent can run immediately.
	autoAssignCredentials(r.Context(), h.db, wsID, agentID, now)

	h.logger.Info("agent created via coordinator", "agent_id", agentID, "name", body.Name, "crew_id", body.CrewID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":          agentID,
		"name":        body.Name,
		"slug":        body.Slug,
		"crew_id":     body.CrewID,
		"workspace_id": wsID,
	})
}

// ListCrewConnections handles GET /api/v1/internal/crew-connections?workspace_id=...
// Used by the sidecar on behalf of COORDINATOR agents.
func (h *InternalHandler) ListCrewConnections(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cc.id, cc.from_crew_id, cc.to_crew_id, cc.direction, cc.status,
		       fc.name, fc.slug, tc.name, tc.slug
		FROM crew_connections cc
		JOIN crews fc ON fc.id = cc.from_crew_id
		JOIN crews tc ON tc.id = cc.to_crew_id
		WHERE cc.workspace_id = ? AND cc.status = 'active'
		ORDER BY cc.created_at DESC`, wsID)
	if err != nil {
		h.logger.Error("list crew connections internal", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type connEntry struct {
		ID           string `json:"id"`
		FromCrewID   string `json:"from_crew_id"`
		FromCrewName string `json:"from_crew_name"`
		FromCrewSlug string `json:"from_crew_slug"`
		ToCrewID     string `json:"to_crew_id"`
		ToCrewName   string `json:"to_crew_name"`
		ToCrewSlug   string `json:"to_crew_slug"`
		Direction    string `json:"direction"`
		Status       string `json:"status"`
	}

	result := []connEntry{}
	for rows.Next() {
		var c connEntry
		if err := rows.Scan(&c.ID, &c.FromCrewID, &c.ToCrewID, &c.Direction, &c.Status,
			&c.FromCrewName, &c.FromCrewSlug, &c.ToCrewName, &c.ToCrewSlug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// RecordMCPToolCall records an MCP tool call audit entry from the sidecar gateway.
func (h *InternalHandler) RecordMCPToolCall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID    string `json:"workspace_id"`
		AgentID        string `json:"agent_id"`
		CrewID         string `json:"crew_id"`
		MCPServerID    string `json:"mcp_server_id"`
		MCPServerScope string `json:"mcp_server_scope"`
		ToolName       string `json:"tool_name"`
		Status         string `json:"status"`
		DurationMS     int64  `json:"duration_ms"`
		ErrorMessage   string `json:"error_message"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.WorkspaceID == "" || body.AgentID == "" || body.MCPServerID == "" || body.ToolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id, agent_id, mcp_server_id, and tool_name are required"})
		return
	}
	if body.MCPServerScope == "" {
		body.MCPServerScope = "workspace"
	}

	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO mcp_tool_calls (id, workspace_id, crew_id, agent_id, mcp_server_id,
			mcp_server_scope, tool_name, status, duration_ms, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		id, body.WorkspaceID, body.CrewID, body.AgentID, body.MCPServerID, body.MCPServerScope,
		body.ToolName, body.Status, body.DurationMS, body.ErrorMessage)
	if err != nil {
		h.logger.Error("record mcp tool call", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to record"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// MCP credential auto-resolution functions are in internal_mcp.go
