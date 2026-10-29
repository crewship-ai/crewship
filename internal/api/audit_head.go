package api

import (
	"database/sql"
	"errors"
	"net/http"
)

// auditHead supplies the exact CAS input for a separately reviewed OWNER
// checkpoint. Reading it does not attest to historical integrity.
func (r *Router) auditHead(w http.ResponseWriter, request *http.Request) {
	scope, workspace := request.URL.Query().Get("scope"), request.URL.Query().Get("workspace_id")
	if (scope != "instance" && scope != "workspace") || (scope == "workspace" && workspace == "") {
		replyError(w, 400, "Scope and matching workspace identity are required")
		return
	}
	tx, err := r.db.BeginTx(request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		r.replyResealFailure(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if err := auditResealAuthority(request, tx, scope, workspace); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	if scope == "instance" && workspace != "" {
		replyError(w, 400, "Instance scope does not accept a workspace identity")
		return
	}
	table, column := "instance_audit_logs", "chain_seq"
	args := []any{}
	where := ""
	if scope == "workspace" {
		table, column = "journal_entries", "seq"
		where = " AND workspace_id=?"
		args = append(args, workspace)
	}
	var seq int64
	var hash string
	err = tx.QueryRowContext(request.Context(), "SELECT "+column+",entry_hash FROM "+table+" WHERE "+column+">0"+where+" ORDER BY "+column+" DESC LIMIT 1", args...).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		r.replyResealFailure(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tail_seq": seq, "tail_hash": hash})
}
