package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// ResolveBedrock supplies one current account to the credential-injecting
// sidecar for a peer run. It is deliberately separate from metadata listings
// and agent-facing Keeper/MCP endpoints. No cached or alternate payer is used.
func (h *InternalHandler) ResolveBedrock(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, crew := InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
	if ws == "" || crew == "" {
		replyError(w, 403, "crew-bound sidecar authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body bedrock.ResolveRequest
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.RunID == "" || body.AgentID == "" || body.AccountID == "" || body.Model == "" {
		replyError(w, 400, "run, agent, account and model required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	defer tx.Rollback()
	// Both the durable start and the current agent must belong to the crew.
	// Moving an agent must not move authority over an old run with it.
	var allowed bool
	err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(
        SELECT 1 FROM agents a
        JOIN journal_entries j ON j.agent_id=a.id
        WHERE a.id=? AND a.workspace_id=? AND a.crew_id=?
          AND a.deleted_at IS NULL AND a.llm_provider='AWS_BEDROCK'
          AND a.cli_adapter='OPENCODE' AND a.llm_model=?
          AND j.trace_id=? AND j.workspace_id=? AND j.crew_id=?
          AND j.entry_type='run.started'
    )`, body.AgentID, ws, crew, bedrock.NativeProvider+"/"+body.Model, body.RunID, ws, crew).Scan(&allowed)
	if err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	if !allowed {
		replyError(w, 403, "Bedrock run is not authorized")
		return
	}
	if _, err := runauthority.Resolve(r.Context(), tx, body.RunID, body.AgentID, ws); err != nil {
		replyError(w, 403, "Bedrock run is not current")
		return
	}
	// Work-backed runs also retain their generation fence. Legacy peer queries
	// have a host-admitted journal identity and no work_attempts row.
	var attempts int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM work_attempts WHERE run_id=?`, body.RunID).Scan(&attempts); err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	if attempts > 0 {
		var live int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM work_attempts wa
            JOIN work_items wi ON wi.id=wa.work_id
            WHERE wa.run_id=? AND wi.workspace_id=? AND wi.crew_id=? AND wi.agent_id=?
              AND wa.ended_at IS NULL AND wa.generation=wi.generation
              AND wi.state IN ('starting','running','waiting')`, body.RunID, ws, crew, body.AgentID).Scan(&live); err != nil || live != 1 {
			replyError(w, 403, "Bedrock run is not current")
			return
		}
	}
	delivered, _, err := loadDeliveredCredentialSnapshot(r.Context(), tx, body.AgentID)
	if err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	var selected *deliveredCredential
	for i := range delivered {
		if delivered[i].Provider == bedrock.Provider {
			if selected != nil {
				replyError(w, 403, "one explicit Bedrock account required")
				return
			}
			selected = &delivered[i]
		}
	}
	if selected == nil || selected.ID != body.AccountID || selected.HandleOnly || selected.Type != "PROVIDER_LOGIN" {
		replyError(w, 403, "assigned Bedrock account unavailable")
		return
	}
	plain, err := encryption.Decrypt(selected.EncryptedValue)
	if err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	account, err := bedrock.Parse(plain)
	if err != nil || account.Expired(time.Now()) || !account.Allows(body.Model) {
		replyError(w, 403, "Bedrock account expired or model not permitted")
		return
	}
	if err := tx.Commit(); err != nil {
		replyError(w, 503, "Bedrock account authority unavailable")
		return
	}
	writeJSON(w, 200, bedrock.ResolveResponse{AccountID: selected.ID, Account: account})
}
