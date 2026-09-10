package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) PublicationHistory(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	before := int64(0)
	if value := r.URL.Query().Get("before"); value != "" {
		var err error
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before < 1 {
			replyError(w, 400, "before must be a positive publication version")
			return
		}
	}
	var live int64
	var published bool
	err := h.db.QueryRowContext(r.Context(), `SELECT version,published FROM page_project_live WHERE page_id=?`, rec.ID).Scan(&live, &published)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read publication state", err)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT p.version,p.build_id,p.source_revision,p.source_digest,p.git_commit,p.artifact_digest,p.created_at,COALESCE(p.actor_user_id,''),COALESCE(p.rollback_of,0),COALESCE(w.created_at,'') FROM page_project_publications p LEFT JOIN page_project_withdrawals w ON w.page_id=p.page_id AND w.version=p.version WHERE p.page_id=? AND (?=0 OR p.version<?) ORDER BY p.version DESC LIMIT 51`, rec.ID, before, before)
	if err != nil {
		replyInternalError(w, h.logger, "read publication history", err)
		return
	}
	defer rows.Close()
	type item struct {
		pagePublication
		Actor       string `json:"actor"`
		RollbackOf  int64  `json:"rollback_of"`
		WithdrawnAt string `json:"withdrawn_at"`
	}
	items := make([]item, 0)
	for rows.Next() {
		var row item
		if err := rows.Scan(&row.Version, &row.BuildID, &row.SourceRevision, &row.SourceDigest, &row.GitCommit, &row.ArtifactDigest, &row.CreatedAt, &row.Actor, &row.RollbackOf, &row.WithdrawnAt); err != nil {
			replyInternalError(w, h.logger, "read publication receipt", err)
			return
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "read publication history", err)
		return
	}
	next := int64(0)
	if len(items) > 50 {
		items = items[:50]
		next = items[49].Version
	}
	user := UserFromContext(r.Context())
	canPublish := user != nil && h.mayAdministerGrants(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, RoleFromContext(r.Context()), rec)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publications": items, "publication_version": live, "published": published, "next_before": next, "can_publish": canPublish})
}

func (h *PageHandler) UnpublishProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	user := UserFromContext(r.Context())
	ws := WorkspaceIDFromContext(r.Context())
	if user == nil || !h.mayAdministerGrants(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "Withdrawing an application requires Page ownership or workspace administration")
		return
	}
	raw, ok := readCapped(w, r, 1024, "Page withdrawal")
	if !ok {
		return
	}
	var req struct {
		Expected int64 `json:"expected_publication" yaml:"expected_publication"`
	}
	if err := pages.DecodeProjectJSON(raw, &req); err != nil || req.Expected < 1 {
		replyError(w, 400, "expected_publication is required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin withdrawal", err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(), `UPDATE page_project_live SET published=0 WHERE page_id=? AND version=?`, rec.ID, req.Expected)
	if err != nil {
		replyInternalError(w, h.logger, "withdraw application", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		replyError(w, 409, "Publication changed; reload before withdrawing")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_project_withdrawals(page_id,version,actor_user_id,created_at) VALUES(?,?,?,?) ON CONFLICT(page_id,version) DO NOTHING`, rec.ID, req.Expected, user.ID, tsformat.Format(time.Now())); err != nil {
		replyInternalError(w, h.logger, "record withdrawal", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit withdrawal", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"slug": rec.Slug, "page_id": rec.ID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publication": nil, "publication_version": req.Expected, "published": false})
}
