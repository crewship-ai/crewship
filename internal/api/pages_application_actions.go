package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type pageApplicationFence struct {
	page, workspace, spec string
	version               int64
}
type pageApplicationFenceKey struct{}

var errPageApplicationChanged = errors.New("Page publication or definition changed; reload the application before starting an action")

func pageApplicationFenceFrom(ctx context.Context) *pageApplicationFence {
	v, _ := ctx.Value(pageApplicationFenceKey{}).(*pageApplicationFence)
	return v
}
func (h *PageHandler) enqueuePageAction(ctx context.Context, run pipeline.PendingRun) (string, bool, error) {
	fence := pageApplicationFenceFrom(ctx)
	if fence == nil {
		return pipeline.NewPendingRunStore(h.db).Enqueue(ctx, run)
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	// This write acquires SQLite's writer lock before enqueue. A publication or
	// panel editor cannot change the declaration between the fence and insertion.
	result, err := tx.ExecContext(ctx, `UPDATE page_project_live SET version=version WHERE page_id=? AND version=? AND published=1 AND EXISTS(SELECT 1 FROM pages WHERE id=? AND workspace_id=? AND spec_json=?)`, fence.page, fence.version, fence.page, fence.workspace, fence.spec)
	if err != nil {
		return "", false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if n != 1 {
		return "", false, errPageApplicationChanged
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(run.MetadataJSON), &metadata); err != nil {
		return "", false, err
	}
	metadata["application_publication"] = fence.version
	var definition, checksJSON string
	if err := tx.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, run.PipelineID, fence.workspace).Scan(&definition); err != nil {
		return "", false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT checks_json FROM page_project_publications WHERE page_id=? AND version=?`, fence.page, fence.version).Scan(&checksJSON); err != nil {
		return "", false, err
	}
	var checks pageRoutineChecks
	if err := json.Unmarshal([]byte(checksJSON), &checks); err != nil {
		return "", false, err
	}
	reviewed := checks.Definitions[run.PipelineSlug]
	current := pageRoutineDigest(definition)
	metadata["application_routine_at_publication"] = reviewed
	metadata["application_routine_at_enqueue"] = current
	metadata["application_routine_changed"] = reviewed != "" && reviewed != current
	metadata["application_routine_revision_pinned"] = false

	data, err := json.Marshal(metadata)
	if err != nil {
		return "", false, err
	}
	run.MetadataJSON = string(data)
	id, coalesced, err := pipeline.NewPendingRunStoreTx(tx).Enqueue(ctx, run)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, coalesced, nil
}
func (h *PageHandler) DispatchApplicationAction(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Unauthorized")
		return
	}
	raw, ok := readCapped(w, r, 32<<10, "Page application action")
	if !ok {
		return
	}
	var request struct {
		Publication int64          `json:"publication" yaml:"publication"`
		Inputs      map[string]any `json:"inputs" yaml:"inputs"`
	}
	if err := pages.DecodeProjectJSON(raw, &request); err != nil || request.Publication < 1 {
		replyError(w, 400, "publication and declared action inputs are required")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, _, err := h.resolvePanelForCaller(r.Context(), ws, user.ID, r.PathValue("slug"), r.PathValue("panelId"))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errActionNotVisible) {
		replyError(w, 404, "Page action not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve application action", err)
		return
	}
	var version int64
	var spec, current string
	err = h.db.QueryRowContext(r.Context(), `SELECT l.version,p.spec_json,g.spec_json FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version JOIN pages g ON g.id=l.page_id WHERE l.page_id=? AND l.published=1`, rec.ID).Scan(&version, &spec, &current)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 409, errPageApplicationChanged.Error())
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve application publication", err)
		return
	}
	if version != request.Publication || current != spec {
		replyError(w, 409, errPageApplicationChanged.Error())
		return
	}
	// Only the stored call action is executable; the child supplies no routine,
	// shell, URL, permission or workspace. DispatchAction rechecks all normal gates.
	var doc pages.Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		replyInternalError(w, h.logger, "decode published actions", err)
		return
	}
	panel, exists := doc.FindPanel(r.PathValue("panelId"))
	if !exists {
		replyError(w, 404, "Page action not found")
		return
	}
	allowed := false
	for _, action := range panel.Actions {
		if action.ID == r.PathValue("actionId") && action.Kind == pages.ActionCall {
			allowed = true
		}
	}
	if !allowed {
		replyError(w, 404, "Published call action not found")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		replyError(w, 400, "Idempotency-Key (at most 128 bytes) is required")
		return
	}
	body, err := json.Marshal(dispatchRequest{Inputs: request.Inputs})
	if err != nil {
		replyError(w, 400, "invalid action inputs")
		return
	}
	ctx := context.WithValue(r.Context(), pageApplicationFenceKey{}, &pageApplicationFence{page: rec.ID, workspace: ws, spec: spec, version: version})
	copy := r.Clone(ctx)
	copy.Body = io.NopCloser(bytes.NewReader(body))
	copy.ContentLength = int64(len(body))
	// A chosen key cannot replay another user's receipt or an older publication.
	copy.Header.Set("Idempotency-Key", fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", user.ID, version, key)))))
	h.DispatchAction(w, copy)
}
func (h *PageHandler) ApplicationActionStatus(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Unauthorized")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, err := h.loadPage(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Page action not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read action Page", err)
		return
	}
	var status, runID, runStatus, panelID, routineDigest string
	var routineChanged bool
	err = h.db.QueryRowContext(r.Context(), `SELECT p.status,COALESCE(p.fired_run_id,''),COALESCE(r.status,''),COALESCE(json_extract(p.metadata_json,'$.panel'),''),COALESCE(json_extract(p.metadata_json,'$.application_routine_at_enqueue'),''),COALESCE(json_extract(p.metadata_json,'$.application_routine_changed'),0) FROM pending_runs p LEFT JOIN pipeline_runs r ON r.id=p.fired_run_id AND r.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=? AND p.invoking_user_id=? AND json_extract(p.metadata_json,'$.page_id')=? AND json_extract(p.metadata_json,'$.application_publication') IS NOT NULL`, r.PathValue("pendingId"), ws, user.ID, rec.ID).Scan(&status, &runID, &runStatus, &panelID, &routineDigest, &routineChanged)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Page action not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read application action status", err)
		return
	}
	if _, _, err := h.resolvePanelForCaller(r.Context(), ws, user.ID, rec.Slug, panelID); err != nil {
		replyError(w, 404, "Page action not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"pending_id": r.PathValue("pendingId"), "pending_status": status, "run_id": runID, "run_status": runStatus, "routine_definition_at_enqueue": routineDigest, "routine_changed_since_publication": routineChanged, "routine_revision_pinned": false})
}
