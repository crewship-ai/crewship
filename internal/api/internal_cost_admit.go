package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// This is a host policy gate, not a reservation protocol. Legacy proxy requests
// carry no trustworthy model/output ceiling or mission attribution, so metered
// calls under hard budgets must use the restricted broker instead.
func (r *Router) handleSidecarCostAdmit(w http.ResponseWriter, req *http.Request) {
	ws := InternalTokenWorkspaceFromContext(req.Context())
	if ws == "" {
		replyError(w, http.StatusForbidden, "workspace-bound internal token required")
		return
	}
	var body struct {
		Agent string `json:"agent_id"`
		// Crew is the identity of the crew-level sidecar a routine script
		// step runs against (#2761): it has no agent. Accepted only when the
		// caller's internal token is bound to that same crew.
		Crew           string `json:"crew_id"`
		Credential     string `json:"credential_id"`
		Provider       string `json:"provider"`
		Run            string `json:"run_id"`
		SourceRevision string `json:"source_revision"`
	}
	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	dec := json.NewDecoder(req.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || body.Provider == "" || (body.Agent == "") == (body.Crew == "") {
		replyError(w, http.StatusBadRequest, "provider and exactly one of agent_id or crew_id required")
		return
	}
	// Scope, grant, budget and run checks share the audit transaction. A
	// concurrent authority change cannot be attributed using an old scope
	// snapshot followed by an audit append against newer state.
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "proxy admission unavailable")
		return
	}
	defer tx.Rollback()
	var crew string
	if body.Crew != "" {
		// Crew-scoped caller (#2761). The identity comes from the token, not
		// the body: an unbound token, or one bound to another crew, cannot
		// claim it. A crew-level sidecar delivers no credentials, so naming
		// one is refused rather than checked. The budget rule below applies
		// unchanged, with no agent scope.
		if bound := InternalTokenCrewFromContext(req.Context()); bound == "" || bound != body.Crew {
			replyError(w, http.StatusForbidden, "crew scope mismatch")
			return
		}
		if body.Credential != "" || body.Run != "" {
			replyError(w, http.StatusForbidden, "a crew-scoped caller holds no credential or run identity")
			return
		}
		err = tx.QueryRowContext(req.Context(), `SELECT id FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, body.Crew, ws).Scan(&crew)
		if err != nil {
			replyError(w, http.StatusForbidden, "crew scope unavailable")
			return
		}
	} else {
		err = tx.QueryRowContext(req.Context(), `SELECT a.crew_id FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND c.deleted_at IS NULL`, body.Agent, ws).Scan(&crew)
		if err != nil {
			replyError(w, http.StatusForbidden, "agent scope unavailable")
			return
		}
		if bound := InternalTokenCrewFromContext(req.Context()); bound != "" && bound != crew {
			replyError(w, http.StatusForbidden, "agent scope mismatch")
			return
		}
	}
	kind := ""
	if body.Credential != "" {
		err = tx.QueryRowContext(req.Context(), `SELECT c.type FROM credentials c
   WHERE c.id=? AND c.workspace_id=? AND c.provider=? AND c.status='ACTIVE' AND c.deleted_at IS NULL AND c.type IN ('API_KEY','AI_CLI_TOKEN','PROVIDER_LOGIN')
   AND (EXISTS (SELECT 1 FROM agent_credentials ac WHERE ac.credential_id=c.id AND ac.agent_id=? AND (ac.expires_at IS NULL OR ac.expires_at>?))
    OR (c.type!='PROVIDER_LOGIN' AND (c.scope='WORKSPACE'
     OR EXISTS (SELECT 1 FROM credential_crews cc WHERE cc.credential_id=c.id AND cc.crew_id=?)
     OR EXISTS (SELECT 1 FROM credential_bindings cb WHERE cb.credential_id=c.id AND cb.workspace_id=c.workspace_id
      AND (cb.scope='WORKSPACE' OR (cb.scope='CREW' AND cb.crew_id=?) OR (cb.scope='AGENT' AND cb.agent_id=?))))))`,
			body.Credential, ws, body.Provider, body.Agent, time.Now().UTC().Format(time.RFC3339), crew, crew, body.Agent).Scan(&kind)
		if err == sql.ErrNoRows && body.Provider == sidecarOpenAICompatProvider {
			// ENDPOINT_URL is projected as OPENAI_COMPAT only in the proxy
			// snapshot. Its stored provider/type stay unchanged. Re-resolve the
			// selected source in this SAME transaction: a stale sidecar cannot
			// authorize a removed grant, changed selection or foreign crew.
			delivered, _, loadErr := loadDeliveredCredentialSnapshot(req.Context(), tx, body.Agent)
			if loadErr == nil {
				assigned := resolvedDeliveredCredentialEntries(r.logger, body.Agent, delivered)
				ep := resolveLocalModelEndpoint(req.Context(), tx, r.logger, body.Agent, ws, assigned)
				collision := false
				for _, c := range assigned {
					if providerNeedsEndpointValue(c.Provider) {
						collision = true
						break
					}
				}
				if !collision && ep.CredentialID == body.Credential && body.SourceRevision != "" && body.SourceRevision == ep.SourceRevision && ep.BaseURL != "" && (ep.APIKey != "" || len(ep.Headers) > 0) {
					kind, err = CredTypeEndpointURL, nil
				}
			}
		}
		if err != nil {
			replyError(w, http.StatusForbidden, "credential scope unavailable")
			return
		}
	}
	// ID-only legacy admission cannot prove the revision of a cached token. A
	// credential changed from API_KEY to PROVIDER_LOGIN may still leave its
	// old metered key in a running sidecar, so billing labels cannot exempt it.
	var found int
	err = tx.QueryRowContext(req.Context(), `SELECT 1 FROM budget_limits WHERE workspace_id=? AND enabled=1 AND mode IN ('hard','tiered')
   AND (scope_kind='workspace' OR (scope_kind='crew' AND scope_id=?) OR (scope_kind='agent' AND scope_id=?) OR scope_kind='mission' OR (?='' AND scope_kind IN ('crew','agent'))) LIMIT 1`, ws, crew, body.Agent, body.Credential).Scan(&found)
	if err != nil && err != sql.ErrNoRows {
		replyError(w, http.StatusServiceUnavailable, "budget admission unavailable")
		return
	}
	if err == nil {
		replyError(w, http.StatusForbidden, "hard-budget traffic requires the restricted broker")
		return
	}
	if body.Run != "" {
		// Attribute only an unambiguous, active journal run in the already
		// validated agent/workspace/crew scope. The event describes
		// admission, not later forwarding.
		var starts, ended int
		var recordedAgent, recordedCrew string
		err = tx.QueryRowContext(req.Context(), `SELECT COUNT(*),COALESCE(MAX(agent_id),''),COALESCE(MAX(crew_id),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type='run.started'`, ws, body.Run).Scan(&starts, &recordedAgent, &recordedCrew)
		if err == nil {
			err = tx.QueryRowContext(req.Context(), `SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, ws, body.Run).Scan(&ended)
		}
		if err != nil {
			replyError(w, http.StatusServiceUnavailable, "run attribution unavailable")
			return
		}
		if starts != 1 || recordedAgent != body.Agent || recordedCrew != crew || ended != 0 {
			replyError(w, http.StatusForbidden, "run scope unavailable")
			return
		}
		if body.Credential != "" {
			err = RecordCredentialEventTx(req.Context(), tx, body.Credential, AuditEventProxyAdmitted, body.Agent, "", map[string]any{
				"source": "sidecar_proxy_admission", "run_id": body.Run, "crew_id": crew,
				"provider": body.Provider, "channel": "proxy", "stage": "admitted", "authentication": "unknown",
			})
			if err != nil {
				replyError(w, http.StatusServiceUnavailable, "proxy audit unavailable")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusServiceUnavailable, "proxy admission unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": true})
}
