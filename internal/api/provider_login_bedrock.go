package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

// Bedrock uses an exact agent binding (explicit or materialized from an
// explicit crew account rule), or an explicit manifest grant.
// Crew/workspace pool priority must never substitute a different billing account.
func selectedBedrockAccount(ctx context.Context, db sqlQuerier, agentID string) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT credential_id FROM credential_bindings WHERE agent_id=? AND scope='AGENT' AND slot=? UNION SELECT credential_id FROM agent_credentials WHERE agent_id=? AND env_var_name=?`, agentID, bedrock.Slot, agentID, bedrock.Slot)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	id := ""
	for rows.Next() {
		var next string
		if err := rows.Scan(&next); err != nil {
			return "", err
		}
		if id != "" && id != next {
			return "", nil
		}
		id = next
	}
	return id, rows.Err()
}

type bedrockAssignmentResponse struct {
	Assigned int    `json:"assigned"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AssignBedrock atomically binds a saved account and configures the native model
// and supported runner. Existing assignments are never silently overwritten.
func (h *CredentialHandler) AssignBedrock(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, 403, "Provider accounts require OWNER or ADMIN")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("credentialId")
	var body struct {
		AgentIDs []string `json:"agent_ids"`
		CrewIDs  []string `json:"crew_ids"`
		Model    string   `json:"model"`
	}
	if readJSON(r, &body) != nil || len(body.AgentIDs)+len(body.CrewIDs) > 100 || len(body.AgentIDs)+len(body.CrewIDs) == 0 {
		replyError(w, 400, "Select agents or crews and one permitted model")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, 500, "Could not start assignment")
		return
	}
	defer tx.Rollback()
	var value, scope string
	err = tx.QueryRowContext(r.Context(), `SELECT encrypted_value,scope FROM credentials WHERE id=? AND workspace_id=? AND provider=? AND type='PROVIDER_LOGIN' AND status='ACTIVE' AND deleted_at IS NULL`, id, ws, bedrock.Provider).Scan(&value, &scope)
	if err != nil {
		replyError(w, 404, "Bedrock account not available")
		return
	}
	clear, err := decryptCredential(value)
	if err != nil {
		replyError(w, 500, "Could not read Bedrock account")
		return
	}
	account, err := bedrock.Parse(clear)
	if err != nil || account.Expired(time.Now()) || !account.Allows(body.Model) {
		replyError(w, 400, "Bedrock account expired or selected model is not permitted")
		return
	}
	targets := map[string]bool{}
	inheritedFrom := map[string]string{}
	crewBindings := map[string]string{}
	for _, a := range body.AgentIDs {
		targets[a] = true
	}
	for _, crew := range body.CrewIDs {

		var belongs bool
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL)`, crew, ws).Scan(&belongs); err != nil || !belongs {
			replyError(w, 404, "Crew not available in this workspace")
			return
		}
		if scope == "CREW" {
			var shared bool
			if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM credential_crews WHERE credential_id=? AND crew_id=?)`, id, crew).Scan(&shared); err != nil || !shared {
				replyError(w, 403, "Bedrock account is not shared with this crew")
				return
			}
		}
		var binding, existingAccount string
		err := tx.QueryRowContext(r.Context(), `SELECT id,credential_id FROM credential_bindings WHERE workspace_id=? AND scope='CREW' AND crew_id=? AND slot=?`, ws, crew, bedrock.Slot).Scan(&binding, &existingAccount)
		if err != nil && err != sql.ErrNoRows {
			replyError(w, 500, "Could not inspect crew account")
			return
		}
		if existingAccount != "" && existingAccount != id {
			replyError(w, 409, "Crew already has a Bedrock account; remove its rule before changing payer")
			return
		}
		if binding == "" {
			binding = generateCUID()
			_, err = tx.ExecContext(r.Context(), `INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,crew_id,slot,created_by,bedrock_model) VALUES(?,?,?,'CREW',?,?,?,?)`, binding, ws, id, crew, bedrock.Slot, UserFromContext(r.Context()).ID, body.Model)
		} else {
			_, err = tx.ExecContext(r.Context(), `UPDATE credential_bindings SET bedrock_model=?,updated_at=? WHERE id=?`, body.Model, time.Now().UTC().Format(time.RFC3339), binding)
		}
		if err != nil {
			replyError(w, 409, "Could not persist crew account rule")
			return
		}
		crewBindings[crew] = binding
		rows, e := tx.QueryContext(r.Context(), `SELECT id FROM agents WHERE crew_id=? AND workspace_id=? AND deleted_at IS NULL`, crew, ws)
		if e != nil {
			replyError(w, 500, "Could not resolve crew agents")
			return
		}
		for rows.Next() {
			var a string
			if rows.Scan(&a) != nil {
				rows.Close()
				replyError(w, 500, "Could not resolve crew agents")
				return
			}
			targets[a] = true
			inheritedFrom[a] = crewBindings[crew]
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			replyError(w, 500, "Could not resolve crew agents")
			return
		}
	}
	if len(targets) > 100 {
		replyError(w, 400, "Assign at most 100 agents at once")
		return
	}
	// Agent IDs explicitly selected alongside a crew are independent overrides.
	for _, agent := range body.AgentIDs {
		delete(inheritedFrom, agent)
	}
	assignedCount := 0
	for agent := range targets {
		var crew sql.NullString
		if tx.QueryRowContext(r.Context(), `SELECT crew_id FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, agent, ws).Scan(&crew) != nil {
			replyError(w, 404, "Agent not available in this workspace")
			return
		}
		if scope == "CREW" {
			var n int
			_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM credential_crews WHERE credential_id=? AND crew_id=?`, id, crew.String).Scan(&n)
			if n == 0 {
				replyError(w, 403, "Bedrock account is not shared with this crew")
				return
			}
		}
		var existing string
		var inherited sql.NullString
		var modelOverridden bool
		e := tx.QueryRowContext(r.Context(), `SELECT credential_id,inherited_from_binding_id,bedrock_model_overridden FROM credential_bindings WHERE workspace_id=? AND scope='AGENT' AND agent_id=? AND slot=?`, ws, agent, bedrock.Slot).Scan(&existing, &inherited, &modelOverridden)
		if e != nil && e != sql.ErrNoRows {
			replyError(w, 500, "Could not inspect model account assignment")
			return
		}
		if inheritedFrom[agent] != "" {
			// A crew rule is a default. Reapplying it must preserve an
			// explicit individual payer/model, including legacy grants.
			var explicitLegacy bool
			if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_credentials WHERE agent_id=? AND env_var_name=?)`, agent, bedrock.Slot).Scan(&explicitLegacy); err != nil {
				replyError(w, 500, "Could not inspect individual account override")
				return
			}
			if (existing != "" && (!inherited.Valid || modelOverridden)) || explicitLegacy {
				continue
			}
			// Same eligibility as the crew triggers: only agents on Bedrock or
			// with no provider (on a runner Bedrock can drive, without a
			// model of another provider) take the crew default.
			var provider, adapter, model string
			if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(llm_provider,''),cli_adapter,COALESCE(llm_model,'') FROM agents WHERE id=? AND workspace_id=?`, agent, ws).Scan(&provider, &adapter, &model); err != nil {
				replyError(w, 500, "Could not inspect agent provider")
				return
			}
			eligible := provider == bedrock.Provider || (provider == "" && (adapter == "CLAUDE_CODE" || adapter == "OPENCODE") && (model == "" || strings.HasPrefix(model, bedrock.NativeProvider+"/")))
			if !eligible {
				continue
			}
		}
		if existing != "" && existing != id {
			replyError(w, 409, "Agent already has a Bedrock account; remove its existing assignment before changing payer")
			return
		}
		var legacy int
		if tx.QueryRowContext(r.Context(), `SELECT count(*) FROM agent_credentials WHERE agent_id=? AND env_var_name=? AND credential_id<>?`, agent, bedrock.Slot, id).Scan(&legacy) != nil {
			replyError(w, 500, "Could not inspect model grant")
			return
		}
		if legacy > 0 {
			replyError(w, 409, "Agent has another explicit Bedrock grant")
			return
		}
		if existing == "" {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,agent_id,slot,created_by) VALUES(?,?,?,'AGENT',?,?,?)`, generateCUID(), ws, id, agent, bedrock.Slot, UserFromContext(r.Context()).ID)
			if err != nil {
				replyError(w, 409, "Could not bind Bedrock account")
				return
			}
		}
		var parent any
		if binding := inheritedFrom[agent]; binding != "" {
			parent = binding
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE credential_bindings SET inherited_from_binding_id=?,bedrock_model_overridden=? WHERE workspace_id=? AND agent_id=? AND scope='AGENT' AND slot=? AND credential_id=?`, parent, parent == nil, ws, agent, bedrock.Slot, id); err != nil {
			replyError(w, 500, "Could not record account inheritance")
			return
		}
		_, err = tx.ExecContext(r.Context(), `UPDATE agents SET cli_adapter='OPENCODE',llm_provider=?,llm_model=?,updated_at=? WHERE id=? AND workspace_id=?`, bedrock.Provider, bedrock.NativeProvider+"/"+body.Model, time.Now().UTC().Format(time.RFC3339), agent, ws)
		if err != nil {
			replyError(w, 500, "Could not configure Bedrock model")
			return
		}
		assignedCount++
	}
	if err = RecordCredentialEventTx(r.Context(), tx, id, AuditEventUse, "", clientIP(r), map[string]any{"action": "bedrock_model_assignment", "model": body.Model, "agent_count": assignedCount, "actor": UserFromContext(r.Context()).ID}); err != nil {
		replyError(w, 500, "Could not record model assignment")
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 500, "Could not commit model assignment")
		return
	}
	writeJSON(w, http.StatusOK, bedrockAssignmentResponse{Assigned: assignedCount, Provider: bedrock.Provider, Model: bedrock.NativeProvider + "/" + strings.TrimSpace(body.Model)})
}
