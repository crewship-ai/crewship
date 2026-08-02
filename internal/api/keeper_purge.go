package api

import (
	"net/http"
)

// HandlePurge is DELETE /api/v1/admin/keeper/requests — the keeper's share of a
// workspace-contents wipe.
//
// `crewship seed --nuke` promises to delete all workspace contents and left
// keeper_requests behind; 115 rows survived a nuke on dev2. That is not
// untidiness. Those rows carry `intent`, which is agent-authored free text, and
// `ollama_prompt`, which is the conversation history the judge was shown. A wipe
// that leaves the full record of what every agent asked for, and the
// conversations around it, has not done what it said.
//
// It falls through both nets by construction:
//
//   - keeper_requests has NO workspace_id. The scope comes from the requesting
//     agent, so nothing can enumerate a workspace's rows directly.
//   - `requesting_agent_id REFERENCES agents(id)` carries no ON DELETE CASCADE,
//     so deleting the agents ORPHANS the rows rather than removing them.
//   - The ledger does have a workspace_id with a cascade, but that cascade fires
//     on deleting the WORKSPACE — and a contents-nuke keeps the workspace.
//
// Scoped through two routes at once, because either alone leaves rows behind:
// the requesting agent's workspace (the normal case, and why nukeAll must call
// this BEFORE it deletes the agents — the same ordering escalations and crew
// runtimes already need), and the ledger's own workspace_id (which reaches rows
// whose agent an earlier nuke already removed). A row with neither is not
// reachable from any workspace, and this reports what it deleted rather than
// claiming the table is empty.
func (h *KeeperHandler) HandlePurge(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace is required")
		return
	}

	// One predicate, used for both tables, so they cannot disagree about which
	// rows belong to this workspace.
	const scope = `
		  kr.requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?)
		  OR kr.id IN (SELECT request_id FROM keeper_request_events WHERE workspace_id = ?)`

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "keeper purge: begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	// The ids are RESOLVED FIRST, before either delete. Neither order works
	// otherwise, and both failures are silent:
	//
	//	ledger first  — an orphan is reachable only through its ledger row, so
	//	                deleting that row first erases the one thing that named
	//	                the workspace, and the request survives.
	//	requests first — a ledger row carrying no workspace_id is reachable only
	//	                through its request, so deleting the request first
	//	                strands it.
	//
	// Resolving up front means both deletes see the same set, which is also what
	// makes the two counts comparable in the response.
	rows, err := tx.QueryContext(r.Context(),
		`SELECT kr.id FROM keeper_requests AS kr WHERE `+scope, wsID, wsID)
	if err != nil {
		replyInternalError(w, h.logger, "keeper purge: resolve scope", err)
		return
	}
	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "keeper purge: scan id", err)
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		replyInternalError(w, h.logger, "keeper purge: resolve scope", err)
		return
	}
	rows.Close()

	// Ledger rows are matched by workspace OR by a request in scope: a row can
	// carry a workspace_id, or predate the column, and both belong to this wipe.
	evArgs := append([]any{wsID}, ids...)
	evSQL := `DELETE FROM keeper_request_events WHERE workspace_id = ?`
	if len(ids) > 0 {
		evSQL += ` OR request_id IN (` + sqlPlaceholders(len(ids)) + `)`
	}
	evRes, err := tx.ExecContext(r.Context(), evSQL, evArgs...)
	if err != nil {
		replyInternalError(w, h.logger, "keeper purge: delete ledger", err)
		return
	}

	var requests int64
	if len(ids) > 0 {
		krRes, err := tx.ExecContext(r.Context(),
			`DELETE FROM keeper_requests WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, ids...)
		if err != nil {
			replyInternalError(w, h.logger, "keeper purge: delete requests", err)
			return
		}
		requests, _ = krRes.RowsAffected()
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "keeper purge: commit", err)
		return
	}

	events, _ := evRes.RowsAffected()
	h.logger.Info("keeper purge", "workspace_id", wsID, "requests", requests, "events", events)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted_requests": requests,
		"deleted_events":   events,
	})
}
