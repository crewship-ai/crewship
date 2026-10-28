package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) DesignTool(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	if !h.designEnabled(r.Context()) {
		designError(w, artifacts.ErrDisabled)
		return
	}
	run, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	bound, err := h.artifactProjectRunPage(r.Context(), run)
	if err != nil || !bound.Valid || h.checkArtifactProjectRunConsent(r.Context(), run, bound.String) != nil {
		designError(w, errDesignAccess)
		return
	}
	var p struct {
		Operation string          `json:"operation"`
		Slug      string          `json:"slug"`
		Payload   json.RawMessage `json:"payload"`
	}
	if !designDecode(w, r, &p, (4<<20)+8192) {
		return
	}
	forward := r.Clone(r.Context())
	forward.Body = io.NopCloser(bytes.NewReader(p.Payload))
	forward.ContentLength = int64(len(p.Payload))
	forward.SetPathValue("slug", p.Slug)
	k, ok := h.designKey(w, forward)
	if !ok {
		return
	}
	if k.PageID != bound.String {
		designError(w, artifacts.ErrVersionNotFound)
		return
	}
	switch p.Operation {
	case "design_get":
		var payload struct {
			Version int64 `json:"version,omitempty"`
		}
		if len(p.Payload) > 0 && designDecodeBytes(p.Payload, &payload) != nil {
			replyError(w, 400, "invalid Design read payload")
			return
		}
		if payload.Version < 0 {
			replyError(w, 400, "invalid Design version")
			return
		}
		copiedURL := *forward.URL
		forward.URL = &copiedURL
		if payload.Version > 0 {
			query := forward.URL.Query()
			query.Set("version", strconv.FormatInt(payload.Version, 10))
			forward.URL.RawQuery = query.Encode()
		}
		h.GetDesign(w, forward)
	case "design_apply":
		h.ApplyDesign(w, forward)
	case "design_snapshot":
		h.SnapshotDesign(w, forward)
	case "design_versions":
		h.DesignVersions(w, forward)
	case "design_restore":
		h.RestoreDesign(w, forward)
	default:
		replyError(w, 400, "unknown Design operation")
	}
}

// A successfully versioned agent response completes only the task bound by the
// trusted triggering message; agents cannot nominate another comment/task.
func (h *PageHandler) designCompleteTask(r *http.Request, k artifacts.VersionKey, version int64) error {
	run, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if run == nil {
		return nil
	}
	return h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, actor string) error {
		var comment sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT comment_id FROM artifact_project_message_intents WHERE run_id=? AND workspace_id=? AND page_id=? AND revoked_at IS NULL`, run.RunID, k.WorkspaceID, k.PageID).Scan(&comment); e == sql.ErrNoRows {
			return nil
		} else if e != nil {
			return e
		}
		if !comment.Valid {
			return nil
		}
		var base int64
		if e := tx.QueryRowContext(ctx, `SELECT base_version FROM artifact_project_comment_tasks WHERE comment_id=? AND workspace_id=? AND page_id=? AND active_run_id=? AND status='working'`, comment.String, k.WorkspaceID, k.PageID, run.RunID).Scan(&base); e != nil {
			return nil
		}
		if version <= base {
			return artifacts.ErrVersionConflict
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at,needs_clarification) VALUES(?,?,?,?,?,?,0)`, generateCUID(), comment.String, version, "Updated the selected Design element.", actor, tsformat.Format(time.Now()))
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE artifact_project_comment_tasks SET status='answered' WHERE comment_id=? AND active_run_id=?`, comment.String, run.RunID)
		return e
	})
}
