package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// The immutable discussion is fetched only after trusted run admission. It is
// never persisted in the chat prompt before the live editor check.
func (h *PageHandler) GetArtifactRequestAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	var page, snapshot, instruction string
	var base int64
	if tx.QueryRowContext(r.Context(), `SELECT ar.page_id,ar.snapshot_json,ar.instruction,ar.base_rev FROM page_artifact_requests ar JOIN pages p ON p.id=ar.page_id AND p.workspace_id=? WHERE ar.id=? AND ar.run_id=? AND ar.agent_id=? AND ar.human_user_id=? AND ar.chat_id=? AND ar.status IN ('running','succeeded')`, a.WorkspaceID, r.PathValue("requestId"), a.RunID, a.AgentID, a.HumanUserID, a.ChatID).Scan(&page, &snapshot, &instruction, &base) != nil {
		replyError(w, 403, "artifact request authority required")
		return
	}
	if err = h.checkArtifactRun(r.Context(), page, a); err != nil || a.RequestID != r.PathValue("requestId") {
		replyError(w, 403, "artifact request authority changed")
		return
	}
	if !json.Valid([]byte(snapshot)) {
		replyError(w, 503, "request snapshot unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("requestId"), "base_rev": base, "instruction": instruction, "snapshot": json.RawMessage(snapshot)})
}
