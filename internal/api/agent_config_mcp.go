package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

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
		fmt.Fprintf(&sb, "<skill name=%q category=%q>\n", displayName, category)
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
	fmt.Fprintf(&keeperBlock, "    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why you need it>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug)
	keeperBlock.WriteString("The Keeper (AI gatekeeper) will evaluate your request and respond with ALLOW or DENY.\n")
	keeperBlock.WriteString("If ALLOW, the response contains the credential value. If DENY, do NOT retry \u2014 explain to the user why access was denied.\n\n")
	keeperBlock.WriteString("To execute a command with a credential (without seeing the value):\n")
	keeperBlock.WriteString("  curl -s -X POST http://localhost:9119/keeper/execute \\\n")
	keeperBlock.WriteString("    -H \"Content-Type: application/json\" \\\n")
	fmt.Fprintf(&keeperBlock, "    -d '{\"credential_name\":\"<NAME>\",\"intent\":\"<why>\",\"command\":\"<shell command>\",\"agent_slug\":\"%s\"}'\n\n", agentSlug)
	keeperBlock.WriteString("Keeper-guarded credentials available to you:\n")
	for _, name := range secretCreds {
		fmt.Fprintf(&keeperBlock, "  - %s\n", name)
	}
	keeperBlock.WriteString("[END CREDENTIAL ACCESS CONTROL]")
	return keeperBlock.String()
}
