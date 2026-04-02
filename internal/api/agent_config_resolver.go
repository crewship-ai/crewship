package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// agentConfigData holds the intermediate state during agent config resolution.
type agentConfigData struct {
	agentID   string
	agentSlug string
	agentName string
	roleTitle sql.NullString
	agentRole sql.NullString
	cliAdapter   string
	toolProfile  string
	wsID         string
	systemPrompt sql.NullString
	llmModel     sql.NullString
	timeoutSecs  int
	memoryEnabled bool

	crewID             sql.NullString
	crewSlug           sql.NullString
	crewName           sql.NullString
	crewNetworkMode    sql.NullString
	crewAllowedDomains sql.NullString
	crewMemoryMB       sql.NullInt64
	crewCPUs           sql.NullFloat64
	crewTTLHours       sql.NullInt64
	crewMCPConfigJSON  sql.NullString
	agentMCPConfigJSON sql.NullString
}

// memberIntegrationEntry describes an MCP integration available to a crew member.
type memberIntegrationEntry struct {
	Name       string   `json:"name"`
	ServerName string   `json:"server_name"`
	Tools      []string `json:"tools"`
}

// crewMemberEntry describes a peer agent within a crew.
type crewMemberEntry struct {
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	Slug         string                   `json:"slug"`
	RoleTitle    string                   `json:"role_title"`
	Description  string                   `json:"description"`
	Status       string                   `json:"status"`
	ChatID       string                   `json:"chat_id,omitempty"`
	Integrations []memberIntegrationEntry `json:"integrations,omitempty"`
}

// crewInfoEntry describes a crew and its agents (used for COORDINATOR context).
type crewInfoEntry struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Slug    string           `json:"slug"`
	Members []crewMemberEntry `json:"members"`
}

// mcpServerEntry describes a resolved MCP server for the agent.
type mcpServerEntry struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Transport   string            `json:"transport"`
	Endpoint    *string           `json:"endpoint,omitempty"`
	Command     *string           `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	CredToken   string            `json:"cred_token,omitempty"`
	CredType    string            `json:"cred_type,omitempty"`
	CredHeader  string            `json:"cred_header,omitempty"`
	EnvVarName  string            `json:"env_var_name,omitempty"`
}

// mcpServerRow is a raw DB row for an MCP server definition.
type mcpServerRow struct {
	id, name, displayName, transport string
	endpoint, command, argsJSON, envJSON *string
}

func (h *InternalHandler) resolveAgentConfig(w http.ResponseWriter, r *http.Request, agentID string) {
	data, err := h.loadAgentData(r, agentID)
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
	if data.crewMCPConfigJSON.Valid && data.crewMCPConfigJSON.String != "" && data.crewID.Valid {
		if err := MigrateJSONBlobToCrewServers(r.Context(), h.db, h.logger, data.crewID.String, data.wsID, data.crewMCPConfigJSON.String); err != nil {
			h.logger.Warn("auto-migrate crew MCP config in resolveAgentConfig", "crew_id", data.crewID.String, "error", err)
		} else {
			data.crewMCPConfigJSON.String = ""
			data.crewMCPConfigJSON.Valid = false
		}
	}

	creds, err := h.resolveAgentCredentials(r, agentID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Auto-resolve credentials referenced in crew/agent MCP configs.
	creds = autoResolveMCPCredentials(r.Context(), h.db, h.logger, data.wsID, creds,
		data.crewMCPConfigJSON.String, data.agentMCPConfigJSON.String)

	sysPrompt, err := h.loadAgentSystemPrompt(r, data, creds, agentID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	crewMembers, err := h.resolveCrewMembers(r, data, agentID)
	if err != nil {
		h.logger.Error("resolve crew members", "error", err)
		// Non-fatal: continue with empty members
	}

	allCrews := h.resolveCoordinatorCrews(r, data)

	networkMode, allowedDomains := h.resolveNetworkPolicy(data)

	memoryMB, cpus, ttlHours := h.resolveContainerResources(data)

	mcpServers := h.resolveAgentMCPServers(r, data, agentID)

	// Auto-resolve credentials from table-based MCP servers' env_json.
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
			creds = autoResolveMCPCredentials(r.Context(), h.db, h.logger, data.wsID, creds, envJsons...)
		}
	}

	// For OAUTH2 credentials that were auto-resolved (client_id/secret),
	// also include the access token so the orchestrator can write tokens.json.
	creds = h.resolveOAuthAccessTokens(r, creds)

	// [KEEPER] section — credential access control instructions
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
	if h.keeperEnabled.Load() {
		keeperBlock := h.buildKeeperBlock(data.agentSlug, creds)
		if keeperBlock != "" {
			sysPrompt += "\n\n" + keeperBlock
		}
	}

	crewIDStr := ""
	crewSlugStr := ""
	if data.crewID.Valid {
		crewIDStr = data.crewID.String
	}
	if data.crewSlug.Valid {
		crewSlugStr = data.crewSlug.String
	}

	llmModelStr := ""
	if data.llmModel.Valid {
		llmModelStr = data.llmModel.String
	}

	resp := map[string]interface{}{
		"agent_id":             agentID,
		"agent_slug":           data.agentSlug,
		"agent_role":           roleStr,
		"crew_id":              crewIDStr,
		"crew_slug":            crewSlugStr,
		"container_id":         "",
		"cli_adapter":          data.cliAdapter,
		"llm_model":            llmModelStr,
		"system_prompt":        sysPrompt,
		"tool_profile":         data.toolProfile,
		"credentials":          creds,
		"timeout_seconds":      data.timeoutSecs,
		"workspace_id":         data.wsID,
		"memory_enabled":       data.memoryEnabled,
		"crew_members":         crewMembers,
		"network_mode":         networkMode,
		"allowed_domains":      allowedDomains,
		"memory_mb":            memoryMB,
		"cpus":                 cpus,
		"ttl_hours":            ttlHours,
		"mcp_servers":          mcpServers,
		"crew_mcp_config_json":  data.crewMCPConfigJSON.String,
		"agent_mcp_config_json": data.agentMCPConfigJSON.String,
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
			data.wsID)
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

// loadAgentData fetches the core agent and crew data from the database.
func (h *InternalHandler) loadAgentData(r *http.Request, agentID string) (*agentConfigData, error) {
	d := &agentConfigData{agentID: agentID}
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
	`, agentID).Scan(&d.agentSlug, &d.agentName, &d.roleTitle, &d.agentRole, &d.cliAdapter, &d.systemPrompt,
		&d.toolProfile, &d.timeoutSecs, &d.memoryEnabled,
		&d.crewID, &d.crewSlug, &d.crewName, &d.wsID, &d.llmModel,
		&d.crewNetworkMode, &d.crewAllowedDomains,
		&d.crewMemoryMB, &d.crewCPUs, &d.crewTTLHours,
		&d.crewMCPConfigJSON, &d.agentMCPConfigJSON)
	return d, err
}

// resolveAgentCredentials fetches and decrypts credentials assigned to the agent.
func (h *InternalHandler) resolveAgentCredentials(r *http.Request, agentID string) ([]mcpCredEntry, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.priority ASC
	`, agentID)
	if err != nil {
		h.logger.Error("resolve agent credentials", "error", err)
		return nil, err
	}
	defer rows.Close()

	var creds []mcpCredEntry
	for rows.Next() {
		var ce mcpCredEntry
		var encValue string
		if err := rows.Scan(&ce.ID, &ce.EnvVar, &ce.Priority, &encValue, &ce.Type); err != nil {
			h.logger.Error("scan credential for resolve", "error", err)
			return nil, err
		}
		dec, err := decryptCredential(encValue)
		if err != nil {
			h.logger.Error("decrypt credential for resolve", "id", ce.ID, "error", err)
			continue
		}
		ce.Value = dec
		creds = append(creds, ce)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (resolve credentials)", "error", err)
		return nil, err
	}
	if creds == nil {
		creds = []mcpCredEntry{}
	}
	return creds, nil
}

// loadAgentSystemPrompt builds the structured system prompt from ethos, language,
// identity, persona, and skills sections.
func (h *InternalHandler) loadAgentSystemPrompt(r *http.Request, data *agentConfigData, creds []mcpCredEntry, agentID string) (string, error) {
	// Build structured system prompt: ethos -> language -> identity -> persona -> skills
	var promptParts []string

	// Resolve agent_role (default to AGENT if unset)
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}

	// [CREWSHIP ETHOS] section -- non-overridable, injected for every agent
	promptParts = append(promptParts, buildEthosBlock(roleStr))

	// [LANGUAGE PREFERENCE] section -- injected when workspace has a preferred language
	var preferredLanguage sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT preferred_language FROM workspaces WHERE id = ?", data.wsID).Scan(&preferredLanguage); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		h.logger.Warn("preferred language lookup failed", "error", err)
	}
	if preferredLanguage.Valid && preferredLanguage.String != "" {
		lang := preferredLanguage.String
		promptParts = append(promptParts, fmt.Sprintf(
			"[LANGUAGE PREFERENCE]\nAlways respond in: %s\nAll output, thinking, and communication must be in %s.\nIf the user writes in a different language, still respond in %s unless explicitly asked otherwise.\n[END LANGUAGE PREFERENCE]",
			lang, lang, lang))
	}

	// [AGENT IDENTITY] section
	identityLines := []string{"[AGENT IDENTITY]"}
	identityLines = append(identityLines, fmt.Sprintf("Name: %s", data.agentName))
	if data.roleTitle.Valid && data.roleTitle.String != "" {
		identityLines = append(identityLines, fmt.Sprintf("Role: %s", data.roleTitle.String))
	}
	if data.crewName.Valid && data.crewName.String != "" {
		identityLines = append(identityLines, fmt.Sprintf("Crew: %s", data.crewName.String))
	}
	promptParts = append(promptParts, strings.Join(identityLines, "\n"))

	// [PERSONA] section -- user-defined system prompt
	if data.systemPrompt.Valid && data.systemPrompt.String != "" {
		promptParts = append(promptParts, "[PERSONA]\n"+data.systemPrompt.String)
	}

	// [SKILLS AVAILABLE] section
	skillBlock, err := h.resolveSkillsBlock(r, creds, agentID)
	if err != nil {
		return "", err
	}
	if skillBlock != "" {
		promptParts = append(promptParts, skillBlock)
	}

	return strings.Join(promptParts, "\n\n"), nil
}

// resolveSkillsBlock builds the [SKILLS AVAILABLE] system prompt section.
func (h *InternalHandler) resolveSkillsBlock(r *http.Request, creds []mcpCredEntry, agentID string) (string, error) {
	const maxSkillsContextChars = 20000
	const skillHeader = "[SKILLS AVAILABLE]\nYou have access to the following skill playbooks. Activate them when the user's\nrequest matches each skill's \"When to Activate\" criteria.\n\n"
	const skillFooter = "\n[END SKILLS AVAILABLE]"
	const skillSeparator = "\n\n"
	// Budget for skill parts only -- excludes header/footer overhead
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
		return "", nil // non-fatal
	}
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
					credLines = append(credLines, fmt.Sprintf("  - %s: configured \u2713", envVar))
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
		return "", err
	}
	if len(skillParts) > 0 {
		return skillHeader + strings.Join(skillParts, skillSeparator) + skillFooter, nil
	}
	return "", nil
}

// resolveCrewMembers fetches peer agents within the same crew and enriches
// LEAD/COORDINATOR agents with MCP integration info.
func (h *InternalHandler) resolveCrewMembers(r *http.Request, data *agentConfigData, agentID string) ([]crewMemberEntry, error) {
	crewMembers := []crewMemberEntry{}
	if !data.crewID.Valid {
		return crewMembers, nil
	}

	memberRows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.name, a.slug, COALESCE(a.role_title, ''), COALESCE(a.description, ''), a.status,
		       COALESCE((SELECT c.id FROM chats c WHERE c.agent_id = a.id AND c.status = 'ACTIVE' ORDER BY c.created_at DESC LIMIT 1), '')
		FROM agents a
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		ORDER BY a.name
	`, data.crewID.String, agentID)
	if err != nil {
		return crewMembers, err
	}
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

	// Enrich crew members with MCP integration info (single batch query)
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
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

	return crewMembers, nil
}

// resolveCoordinatorCrews loads all workspace crews and their agents for COORDINATOR agents.
func (h *InternalHandler) resolveCoordinatorCrews(r *http.Request, data *agentConfigData) []crewInfoEntry {
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
	if roleStr != "COORDINATOR" {
		return nil
	}

	crewRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`,
		data.wsID)
	if err != nil {
		h.logger.Error("query crews for coordinator", "error", err)
		return nil
	}
	defer crewRows.Close()

	var allCrews []crewInfoEntry
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
	return allCrews
}

// resolveNetworkPolicy determines the network mode and allowed domains for the agent's crew.
func (h *InternalHandler) resolveNetworkPolicy(data *agentConfigData) (string, []string) {
	crewIDStr := ""
	if data.crewID.Valid {
		crewIDStr = data.crewID.String
	}

	networkMode := "free"
	if data.crewNetworkMode.Valid && data.crewNetworkMode.String != "" {
		mode := data.crewNetworkMode.String
		if mode == "free" || mode == "restricted" {
			networkMode = mode
		} else {
			// Unknown mode in DB -- fail closed to prevent silent egress
			h.logger.Error("unknown network_mode in DB, defaulting to restricted", "mode", mode, "crew_id", crewIDStr)
			networkMode = "restricted"
		}
	}
	allowedDomains := []string{}
	if data.crewAllowedDomains.Valid && data.crewAllowedDomains.String != "" {
		if err := json.Unmarshal([]byte(data.crewAllowedDomains.String), &allowedDomains); err != nil {
			h.logger.Error("malformed allowed_domains JSON in DB, defaulting to empty", "error", err, "crew_id", crewIDStr)
			allowedDomains = []string{}
		}
	}
	return networkMode, allowedDomains
}

// resolveContainerResources extracts container resource limits from crew data.
func (h *InternalHandler) resolveContainerResources(data *agentConfigData) (int, float64, int) {
	memoryMB := 4096
	if data.crewMemoryMB.Valid {
		memoryMB = int(data.crewMemoryMB.Int64)
	}
	cpus := 2.0
	if data.crewCPUs.Valid {
		cpus = data.crewCPUs.Float64
	}
	ttlHours := 0
	if data.crewTTLHours.Valid {
		ttlHours = int(data.crewTTLHours.Int64)
	}
	return memoryMB, cpus, ttlHours
}

// resolveAgentMCPServers resolves MCP servers using workspace -> crew -> agent cascade.
func (h *InternalHandler) resolveAgentMCPServers(r *http.Request, data *agentConfigData, agentID string) []mcpServerEntry {
	var mcpServers []mcpServerEntry

	// Step 1: Workspace MCP servers (keyed by name)
	merged := make(map[string]*mcpServerRow)
	if wsRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, display_name, transport, endpoint, command, args_json, env_json
		FROM workspace_mcp_servers WHERE workspace_id = ? AND enabled = 1 AND deleted_at IS NULL`, data.wsID); err == nil {
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
	if data.crewID.Valid {
		if crewRows, err := h.db.QueryContext(r.Context(), `
			SELECT cs.id, cs.name, cs.display_name, cs.transport, cs.endpoint, cs.command, cs.args_json, cs.env_json
			FROM crew_mcp_servers cs
			JOIN crews c ON c.id = cs.crew_id AND c.deleted_at IS NULL
			WHERE cs.crew_id = ? AND cs.enabled = 1 AND cs.deleted_at IS NULL`, data.crewID.String); err == nil {
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
	credTokens := make(map[string]string) // credID -> plaintext
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
					if plain, err := decryptCredential(encVal); err == nil {
						credTokens[cid] = plain
					}
				}
			}
			credRows.Close()
		}
	}

	// Step 4b: Check which servers have ANY bindings (for opt-in filtering).
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
			// Server has bindings for other agents but NOT this one -> skip
			continue
		}
		mcpServers = append(mcpServers, entry)
	}

	return mcpServers
}

// resolveOAuthAccessTokens ensures OAUTH2 credentials include their access tokens
// so the orchestrator can write tokens.json.
func (h *InternalHandler) resolveOAuthAccessTokens(r *http.Request, creds []mcpCredEntry) []mcpCredEntry {
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
			if dec, err := decryptCredential(encVal); err == nil && dec != "" {
				creds = append(creds, mcpCredEntry{
					ID:     credID,
					EnvVar: "_OAUTH_ACCESS_TOKEN:" + credID,
					Value:  dec,
					Type:   "OAUTH2",
				})
			}
		}
	}
	return creds
}

// buildKeeperBlock builds the [CREDENTIAL ACCESS CONTROL] system prompt section
// for agents with Keeper-guarded SECRET credentials. Returns empty string if no
// SECRET credentials exist.
func (h *InternalHandler) buildKeeperBlock(agentSlug string, creds []mcpCredEntry) string {
	var secretCreds []string
	for i := range creds {
		if creds[i].Type == "SECRET" {
			secretCreds = append(secretCreds, creds[i].EnvVar)
			creds[i].Value = ""
		}
	}
	if len(secretCreds) == 0 {
		return ""
	}

	var keeperBlock strings.Builder
	keeperBlock.WriteString("[CREDENTIAL ACCESS CONTROL \u2014 KEEPER]\n")
	keeperBlock.WriteString("Some credentials require explicit approval before use.\n")
	keeperBlock.WriteString("You do NOT have these credentials in your environment. To access them:\n\n")
	keeperBlock.WriteString("  curl -s -X POST http://localhost:9119/keeper/request \\\n")
	keeperBlock.WriteString("    -H \"Content-Type: application/json\" \\\n")
	keeperBlock.WriteString(fmt.Sprintf("    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why you need it>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug))
	keeperBlock.WriteString("The Keeper (AI gatekeeper) will evaluate your request and respond with ALLOW or DENY.\n")
	keeperBlock.WriteString("If ALLOW, the response contains the credential value. If DENY, do NOT retry \u2014 explain to the user why access was denied.\n\n")
	keeperBlock.WriteString("To execute a command with a credential (without seeing the value):\n")
	keeperBlock.WriteString("  curl -s -X POST http://localhost:9119/keeper/execute \\\n")
	keeperBlock.WriteString("    -H \"Content-Type: application/json\" \\\n")
	keeperBlock.WriteString(fmt.Sprintf("    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why>\",\"command\":\"<shell command>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug))
	keeperBlock.WriteString("Keeper-guarded credentials available to you:\n")
	for _, name := range secretCreds {
		keeperBlock.WriteString(fmt.Sprintf("  - %s\n", name))
	}
	keeperBlock.WriteString("[END CREDENTIAL ACCESS CONTROL]")
	return keeperBlock.String()
}
