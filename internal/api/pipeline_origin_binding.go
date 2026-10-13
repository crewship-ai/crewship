package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type routineOriginScheduleBinding struct {
	ID       string  `json:"id"`
	OriginID *string `json:"origin_id"`
}
type routineOriginBindingResponse struct {
	PipelineID string                         `json:"pipeline_id"`
	OriginID   *string                        `json:"origin_id"`
	Schedules  []routineOriginScheduleBinding `json:"schedules"`
}
type routineOriginClearRequest struct {
	Action           string          `json:"action"`
	Reason           string          `json:"reason"`
	ExpectedOriginID json.RawMessage `json:"expected_origin_id"`
	Schedules        []struct {
		ID               string          `json:"id"`
		ExpectedOriginID json.RawMessage `json:"expected_origin_id"`
	} `json:"schedules,omitempty"`
}

func (h *PipelineHandler) operatorOriginAccess(w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	kind := AuthKindFromContext(r.Context())
	if user == nil || (kind != AuthKindSession && kind != AuthKindCLIToken) || r.Context().Value(ctxTokenScopes) != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" {
		replyError(w, 403, "operator authentication required")
		return false
	}
	var role, mode string
	if err := h.db.QueryRowContext(r.Context(), `SELECT wm.role,wm.access_mode FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL JOIN workspaces w ON w.id=wm.workspace_id AND w.deleted_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=?`, WorkspaceIDFromContext(r.Context()), user.ID).Scan(&role, &mode); err != nil || (role != "OWNER" && role != "ADMIN") || mode != "trusted" {
		replyError(w, 403, "OWNER/ADMIN operator required")
		return false
	}
	return true
}
func decodeExpectedOrigin(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var id *string
	if json.Unmarshal(raw, &id) != nil {
		return "", false
	}
	if id == nil {
		return "", true
	}
	if *id == "" {
		return "", false
	}
	return *id, true
}

// GetOriginBinding is operator-only. Ordinary routine responses never expose
// internal authority IDs; this explicit CAS surface needs them for review.
func (h *PipelineHandler) GetOriginBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.operatorOriginAccess(w, r) {
		return
	}
	var out routineOriginBindingResponse
	out.Schedules = []routineOriginScheduleBinding{}
	err := h.db.QueryRowContext(r.Context(), `SELECT id,chat_origin_id FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, WorkspaceIDFromContext(r.Context()), r.PathValue("slug")).Scan(&out.PipelineID, &out.OriginID)
	if err == sql.ErrNoRows {
		replyError(w, 404, "Routine not found")
		return
	}
	if err != nil {
		replyError(w, 500, "Could not read origin binding")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,chat_origin_id FROM pipeline_schedules WHERE workspace_id=? AND target_pipeline_id=? ORDER BY id`, WorkspaceIDFromContext(r.Context()), out.PipelineID)
	if err != nil {
		replyError(w, 500, "Could not read schedule bindings")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item routineOriginScheduleBinding
		if err = rows.Scan(&item.ID, &item.OriginID); err != nil {
			replyError(w, 500, "Could not read schedule bindings")
			return
		}
		out.Schedules = append(out.Schedules, item)
	}
	if rows.Err() != nil {
		replyError(w, 500, "Could not read schedule bindings")
		return
	}
	writeJSON(w, 200, out)
}

// ClearOriginBinding is a conscious replacement of a stale chat-scoped grant
// by the routine's existing standing execution policy. It cannot change
// immutable versions or work already admitted under a receipt.
func (h *PipelineHandler) ClearOriginBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.operatorOriginAccess(w, r) {
		return
	}
	var in routineOriginClearRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || in.Action != "clear" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 || len(in.Schedules) > 100 {
		replyError(w, 400, "clear action, reason and explicit expected bindings required")
		return
	}
	expected, ok := decodeExpectedOrigin(in.ExpectedOriginID)
	if !ok {
		replyError(w, 400, "expected_origin_id must be an origin ID or explicit null")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, 500, "Could not clear binding")
		return
	}
	defer tx.Rollback()
	// Recheck operator authority inside the same transaction as the grant
	// replacement and audit, rather than using a middleware role cache.
	var role, mode string
	if err = tx.QueryRowContext(r.Context(), `SELECT wm.role,wm.access_mode FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL JOIN workspaces w ON w.id=wm.workspace_id AND w.deleted_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&role, &mode); err != nil || (role != "OWNER" && role != "ADMIN") || mode != "trusted" {
		replyError(w, 403, "OWNER/ADMIN operator required")
		return
	}
	var id, current string
	if err = tx.QueryRowContext(r.Context(), `SELECT id,COALESCE(chat_origin_id,'') FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, WorkspaceIDFromContext(r.Context()), r.PathValue("slug")).Scan(&id, &current); err == sql.ErrNoRows {
		replyError(w, 404, "Routine not found")
		return
	} else if err != nil {
		replyError(w, 500, "Could not read binding")
		return
	}
	if current != expected {
		replyError(w, 409, "origin_binding_conflict")
		return
	}
	seen := map[string]bool{}
	for _, item := range in.Schedules {
		want, valid := decodeExpectedOrigin(item.ExpectedOriginID)
		if !valid || item.ID == "" || seen[item.ID] {
			replyError(w, 400, "each schedule requires a unique ID and expected_origin_id")
			return
		}
		seen[item.ID] = true
		var actual string
		if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(chat_origin_id,'') FROM pipeline_schedules WHERE id=? AND target_pipeline_id=? AND workspace_id=?`, item.ID, id, WorkspaceIDFromContext(r.Context())).Scan(&actual); err == sql.ErrNoRows {
			replyError(w, 404, "Schedule not found")
			return
		} else if err != nil {
			replyError(w, 500, "Could not read schedule binding")
			return
		}
		if actual != want {
			replyError(w, 409, "origin_binding_conflict")
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE pipelines SET chat_origin_id=NULL WHERE id=?`, id); err != nil {
		replyError(w, 500, "Could not clear binding")
		return
	}
	for _, item := range in.Schedules {
		if _, err = tx.ExecContext(r.Context(), `UPDATE pipeline_schedules SET chat_origin_id=NULL WHERE id=?`, item.ID); err != nil {
			replyError(w, 500, "Could not clear schedule binding")
			return
		}
	}
	metadata, _ := json.Marshal(in)
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, "routine.origin_binding.clear", "pipeline", id, string(metadata), time.Now().UTC().Format(time.RFC3339)); err != nil {
		replyError(w, 500, "Could not audit binding clear")
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 500, "Could not clear binding")
		return
	}
	h.GetOriginBinding(w, r)
}
