package api

import (
	"database/sql"
	"net/http"
	"time"
)

type agentCredentialResponse struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
	CredName     string `json:"credential_name"`
	CredType     string `json:"credential_type"`
	CredProvider string `json:"credential_provider"`
	CredStatus   string `json:"credential_status"`
	EnvVarName   string `json:"env_var_name"`
	Priority     int    `json:"priority"`
	CreatedAt    string `json:"created_at"`
	// ExpiresAt is the grant's lease expiry (RFC3339 UTC), empty for a
	// standing grant. Expired reports whether that lease has already lapsed —
	// an expired grant is refused at credential-injection time (fail-closed).
	ExpiresAt string `json:"expires_at,omitempty"`
	Expired   bool   `json:"expired"`
	// LeaseSource is the provenance of the lease: "manual" (an operator's
	// ttl_seconds), "keeper_allow" (auto-issued on a Keeper ALLOW) or
	// "escalation_approve" (auto-issued when a human approved an agent-proposed
	// credential). Empty for a standing grant and for pre-v165 leases. Without
	// it an operator sees a grant expiring with no way to tell what set it.
	LeaseSource string `json:"lease_source,omitempty"`
	// LeaseIssuedAt is when the lease was minted (RFC3339 UTC), empty when unset.
	LeaseIssuedAt string `json:"lease_issued_at,omitempty"`
	// Source distinguishes a grant an operator made from one the agent inherits
	// by belonging to a crew: "explicit" for an agent_credentials row, "crew"
	// for a credential_crews link resolved through the agent's own crew.
	//
	// It exists because the two are revoked in different places. An explicit
	// grant has an assignment id and DELETE /agents/{id}/credentials/{assignmentId}
	// removes it; a crew-derived one has no assignment row at all, and the only
	// way to take it away is to unlink the credential from the crew. Without
	// this field the UI would offer a revoke button that silently does nothing.
	GrantSource string `json:"grant_source"`
}

// ListCredentials returns all credentials assigned to the specified agent.
// GET /api/v1/agents/{agentId}/credentials
func (h *AgentHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check agent exists", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// COALESCE the nullable text columns: a credential may legitimately have a
	// NULL provider/type/status (e.g. a SECRET with no provider, or a row mid
	// lifecycle), and ac.env_var_name/created_at can be NULL on older rows.
	// Scanning a NULL into a Go string returns "converting NULL to string is
	// unsupported" and 500s the whole list — so normalize to '' in SQL.
	// The crew half mirrors agentDeliveredCredentialsSQL, and it has to: this is
	// the only surface — API or CLI — that answers "what does this agent get?",
	// and after the crew fanout it answered wrongly. The agent boots with the
	// crew's credential and this endpoint reported none, which is worse than the
	// original bug because it reads as authoritative to whoever is debugging.
	//
	// It is not the same query, though, and cannot be. That one returns
	// encrypted material for injection; this one is a management listing and
	// must never carry a value. The shared shape is the SET, not the columns —
	// so the two are kept aligned by TestCrewDelivery_ListCredentialsShowsCrewDerived
	// rather than by a constant.
	//
	// Crew rows carry no assignment id, no chosen priority and no lease: there is
	// no agent_credentials row behind them. Empty id + source='crew' says so.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.id, ac.agent_id, ac.credential_id,
			COALESCE(c.name, ''), COALESCE(c.type, ''), COALESCE(c.provider, ''), COALESCE(c.status, ''),
			COALESCE(ac.env_var_name, ''), ac.priority, COALESCE(ac.created_at, ''),
			COALESCE(ac.expires_at, ''), COALESCE(ac.lease_source, ''), COALESCE(ac.lease_issued_at, ''),
			'explicit' AS grant_source
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?

		UNION ALL

		SELECT '' AS id, a.id AS agent_id, c.id AS credential_id,
			COALESCE(c.name, ''), COALESCE(c.type, ''), COALESCE(c.provider, ''), COALESCE(c.status, ''),
			COALESCE(c.name, '') AS env_var_name, 0 AS priority, COALESCE(c.created_at, ''),
			'' AS expires_at, '' AS lease_source, '' AS lease_issued_at,
			'crew' AS grant_source
		FROM agents a
		JOIN credential_crews cc ON cc.crew_id = a.crew_id
		JOIN credentials c ON c.id = cc.credential_id
		WHERE a.id = ? AND a.deleted_at IS NULL AND a.crew_id IS NOT NULL
		  AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
		  AND c.workspace_id = a.workspace_id
		  AND NOT EXISTS (
		      SELECT 1 FROM agent_credentials ac2
		      WHERE ac2.agent_id = a.id AND ac2.credential_id = c.id
		  )
		  -- Suppress the crew-link row when a binding ALREADY DELIVERS this
		  -- credential — i.e. it has a binding that WINS its slot. Mirrors the
		  -- delivery query's crew arm, which suppresses against resolved (not
		  -- merely applicable) bindings. Without this the credential is listed
		  -- twice — once as the binding slot, once as its crew name — while
		  -- delivery hands it over once, so the listing showed a phantom
		  -- variable no container sets. A binding that LOSES its slot does NOT
		  -- suppress: the crew link still delivers it, and this must agree.
		  AND NOT EXISTS (
		      SELECT 1 FROM credential_bindings b
		      WHERE b.workspace_id = a.workspace_id AND b.credential_id = c.id
		        AND (   (b.scope = 'AGENT'     AND b.agent_id = a.id)
		             OR (b.scope = 'CREW'      AND b.crew_id  = a.crew_id)
		             OR  b.scope = 'WORKSPACE')
		        AND NOT EXISTS (
		            SELECT 1 FROM credential_bindings b2
		            WHERE b2.workspace_id = a.workspace_id AND b2.slot = b.slot
		              AND (   (b2.scope = 'AGENT' AND b2.agent_id = a.id AND b.scope IN ('CREW','WORKSPACE'))
		                   OR (b2.scope = 'CREW'  AND b2.crew_id  = a.crew_id AND b.scope = 'WORKSPACE'))
		              AND b2.id != b.id
		        )
		  )

		UNION ALL

		-- Bindings, the fourth source. Reported separately from a crew link
		-- because they are removed differently: credential unbind takes this
		-- one away, unlinking the crew takes the other. Telling an operator
		-- only that a grant is "not explicit" leaves them guessing which.
		--
		-- The slot, not the credential name, is the env var here — that
		-- separation is the entire point of a binding, and reporting the name
		-- would show a variable no container ever sets.
		SELECT '' AS id, a.id AS agent_id, c.id AS credential_id,
			COALESCE(c.name, ''), COALESCE(c.type, ''), COALESCE(c.provider, ''), COALESCE(c.status, ''),
			b.slot AS env_var_name, 0 AS priority, COALESCE(c.created_at, ''),
			'' AS expires_at, '' AS lease_source, '' AS lease_issued_at,
			'binding' AS grant_source
		FROM agents a
		JOIN credential_bindings b
		  ON b.workspace_id = a.workspace_id
		 AND (
		      (b.scope = 'AGENT'     AND b.agent_id = a.id)
		   OR (b.scope = 'CREW'      AND b.crew_id  = a.crew_id)
		   OR (b.scope = 'WORKSPACE')
		 )
		JOIN credentials c ON c.id = b.credential_id
		WHERE a.id = ? AND a.deleted_at IS NULL
		  AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
		  AND c.workspace_id = a.workspace_id
		  AND NOT EXISTS (
		      SELECT 1 FROM agent_credentials ac3
		      WHERE ac3.agent_id = a.id AND ac3.credential_id = c.id
		  )
		  -- Most specific scope wins, the same order delivery resolves in
		  -- (agent > crew > workspace). Without this a workspace binding would
		  -- be listed alongside the crew binding that actually shadows it.
		  AND NOT EXISTS (
		      SELECT 1 FROM credential_bindings b2
		      WHERE b2.workspace_id = a.workspace_id AND b2.slot = b.slot
		        AND (
		             (b2.scope = 'AGENT' AND b2.agent_id = a.id)
		          OR (b2.scope = 'CREW'  AND b2.crew_id  = a.crew_id AND b.scope = 'WORKSPACE')
		        )
		        AND b2.id != b.id
		  )

		ORDER BY env_var_name, priority DESC
	`, agentID, agentID, agentID)
	if err != nil {
		replyInternalError(w, h.logger, "list agent credentials", err)
		return
	}
	defer rows.Close()

	var result []agentCredentialResponse
	for rows.Next() {
		var c agentCredentialResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.CredentialID, &c.CredName,
			&c.CredType, &c.CredProvider, &c.CredStatus,
			&c.EnvVarName, &c.Priority, &c.CreatedAt, &c.ExpiresAt,
			&c.LeaseSource, &c.LeaseIssuedAt, &c.GrantSource); err != nil {
			replyInternalError(w, h.logger, "scan agent credential", err)
			return
		}
		// A lease with expires_at at or before now has lapsed; injection paths
		// refuse it, so surface it as expired to the CLI/UI.
		if c.ExpiresAt != "" {
			if exp, perr := time.Parse(time.RFC3339, c.ExpiresAt); perr == nil && !time.Now().Before(exp) {
				c.Expired = true
			}
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (agent credentials)", err)
		return
	}
	if result == nil {
		result = []agentCredentialResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

type addAgentCredentialRequest struct {
	CredentialID string `json:"credential_id"`
	EnvVarName   string `json:"env_var_name"`
	Priority     int    `json:"priority"`
	// TTLSeconds, when > 0, makes this a short-lived lease: the grant is set
	// to expire TTLSeconds from now and is refused at injection time once it
	// lapses (#1373). 0 (the default) creates a standing grant.
	TTLSeconds int64 `json:"ttl_seconds"`
}

// maxCredentialLeaseSeconds caps a lease at 30 days. A lease is a
// session/short-lived construct; a multi-month "lease" is a standing grant in
// disguise and defeats the ephemerality guarantee. Callers wanting longer just
// omit the TTL (standing grant).
const maxCredentialLeaseSeconds = 30 * 24 * 60 * 60

// AddCredential assigns an existing credential to an agent with a specified environment variable name.
// POST /api/v1/agents/{agentId}/credentials
func (h *AgentHandler) AddCredential(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	foundAgent, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check agent exists", err)
		return
	}
	if !foundAgent {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	var req addAgentCredentialRequest
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.EnvVarName == "" {
		replyError(w, http.StatusBadRequest, "credential_id and env_var_name are required")
		return
	}
	if req.TTLSeconds < 0 {
		replyError(w, http.StatusBadRequest, "ttl_seconds must not be negative")
		return
	}
	if req.TTLSeconds > maxCredentialLeaseSeconds {
		replyError(w, http.StatusBadRequest, "ttl_seconds exceeds the maximum lease of 30 days")
		return
	}

	// Verify credential exists in this workspace (single query prevents enumeration)
	foundCred, err := credentialExists(r.Context(), h.db, req.CredentialID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check credential exists", err)
		return
	}
	if !foundCred {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	id := generateCUID()

	// NULL expires_at = standing grant; a positive TTL makes it a short-lived
	// lease refused at injection time once it lapses (#1373). An explicitly-set
	// TTL is recorded with lease_source 'manual' so it is distinguishable from an
	// auto-issued one — and so issueCredentialLease's "never shorten a longer
	// lease" rule has provenance to preserve.
	var expiresAt, leaseSource, leaseIssuedAt sql.NullString
	if req.TTLSeconds > 0 {
		expiresAt = sql.NullString{
			String: now.Add(time.Duration(req.TTLSeconds) * time.Second).Format(time.RFC3339),
			Valid:  true,
		}
		leaseSource = sql.NullString{String: leaseSourceManual, Valid: true}
		leaseIssuedAt = sql.NullString{String: nowStr, Valid: true}
	}

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at, expires_at, lease_source, lease_issued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agentID, req.CredentialID, req.EnvVarName, req.Priority, nowStr, expiresAt, leaseSource, leaseIssuedAt)
	if err != nil {
		h.logger.Error("add agent credential", "error", err)
		replyError(w, http.StatusConflict, "Credential already assigned to agent")
		return
	}

	// #1198: a human may grant an agent's credential need by creating +
	// assigning the credential directly instead of using `escalation
	// resolve --action approve` on the specific escalation record. Close
	// out any PENDING escalation this agent raised that clearly named this
	// credential, so the queue doesn't accumulate stale, functionally-done
	// rows. Best-effort — never fails the assignment itself.
	var credName sql.NullString
	if scanErr := h.db.QueryRowContext(r.Context(),
		`SELECT name FROM credentials WHERE id = ? AND workspace_id = ?`,
		req.CredentialID, workspaceID).Scan(&credName); scanErr != nil {
		h.logger.Warn("auto-resolve escalations: lookup credential name", "error", scanErr, "credential_id", req.CredentialID)
	} else if credName.Valid {
		autoResolveEscalationsForCredential(r.Context(), h.db, h.logger, h.hub, h.journal, workspaceID, agentID, credName.String)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// RemoveCredential unassigns a credential from an agent.
// DELETE /api/v1/agents/{agentId}/credentials/{credentialId}
func (h *AgentHandler) RemoveCredential(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")
	agentID := r.PathValue("agentId")
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM agent_credentials WHERE id = ? AND agent_id = ?
		 AND agent_id IN (SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL)`,
		assignmentID, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "remove agent credential", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		replyError(w, http.StatusNotFound, "Assignment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
