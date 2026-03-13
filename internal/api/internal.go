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

	var agentID, agentSlug, agentName, cliAdapter, toolProfile, wsID string
	var systemPrompt, roleTitle, agentRole, llmModel sql.NullString
	var timeoutSecs int
	var memoryEnabled bool
	var crewID, crewSlug, crewName sql.NullString
	var crewNetworkMode, crewAllowedDomains sql.NullString

	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.slug, a.name, a.role_title, a.agent_role, a.cli_adapter, a.system_prompt,
			a.tool_profile, a.timeout_seconds, a.memory_enabled,
			c2.id, c2.slug, c2.name, c.workspace_id, a.llm_model,
			c2.network_mode, c2.allowed_domains
		FROM chats c
		JOIN agents a ON a.id = c.agent_id
		LEFT JOIN crews c2 ON c2.id = a.crew_id
		WHERE c.id = ?
	`, chatID).Scan(&agentID, &agentSlug, &agentName, &roleTitle, &agentRole, &cliAdapter, &systemPrompt,
		&toolProfile, &timeoutSecs, &memoryEnabled,
		&crewID, &crewSlug, &crewName, &wsID, &llmModel,
		&crewNetworkMode, &crewAllowedDomains)
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

	// Query crew members for all agents in a crew (enables peer communication)
	type crewMemberEntry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		RoleTitle   string `json:"role_title"`
		Description string `json:"description"`
		Status      string `json:"status"`
		ChatID      string `json:"chat_id,omitempty"`
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
