package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// ArtifactStorageAdminHandler lets an instance administrator read and set the
// aggregate artifact storage limits and rebuild the trigger-maintained counters
// from the source tables. Nothing here deletes artifact data.
type ArtifactStorageAdminHandler struct {
	r *Router
}

func (h *ArtifactStorageAdminHandler) fail(w http.ResponseWriter, req *http.Request, what string, err error) {
	internalError(w, req, h.r.logger, what, err)
}

// Get is GET /api/v1/admin/instance/artifact-storage.
func (h *ArtifactStorageAdminHandler) Get(w http.ResponseWriter, req *http.Request) {
	out, err := artifacts.LoadInstanceStorage(req.Context(), h.r.db, 0)
	if err != nil {
		h.fail(w, req, "load artifact storage", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Put is PUT /api/v1/admin/instance/artifact-storage:
//
//	{ "workspace_limit_bytes": 34359738368, "instance_limit_bytes": 68719476736 }
//
// Either key may be omitted to keep its value. Lowering a limit below current
// usage only refuses further growth; it never removes data.
func (h *ArtifactStorageAdminHandler) Put(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Workspace *int64 `json:"workspace_limit_bytes"`
		Instance  *int64 `json:"instance_limit_bytes"`
	}
	dec := json.NewDecoder(io.LimitReader(req.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx := req.Context()
	tx, err := h.r.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, req, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		h.fail(w, req, "fence", err)
		return
	}
	before, after, err := artifacts.SetStorageLimits(ctx, tx, body.Workspace, body.Instance)
	if errors.Is(err, artifacts.ErrStorageLimit) {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.fail(w, req, "set artifact storage limits", err)
		return
	}
	if err = auditInstance(ctx, req, tx, "instance.artifact_storage_limits_set", "artifact_storage", "", "", map[string]any{
		"workspace_limit_bytes_from": before.WorkspaceLimitBytes, "workspace_limit_bytes_to": after.WorkspaceLimitBytes,
		"instance_limit_bytes_from": before.InstanceLimitBytes, "instance_limit_bytes_to": after.InstanceLimitBytes,
	}); err != nil {
		h.fail(w, req, "audit", err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.fail(w, req, "commit", err)
		return
	}
	out, err := artifacts.LoadInstanceStorage(ctx, h.r.db, 0)
	if err != nil {
		h.fail(w, req, "load artifact storage", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Recompute is POST /api/v1/admin/instance/artifact-storage/recompute with an
// optional {"dry_run": true}. It reports the drift it found (before/after).
func (h *ArtifactStorageAdminHandler) Recompute(w http.ResponseWriter, req *http.Request) {
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if req.Body != nil {
		dec := json.NewDecoder(io.LimitReader(req.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			replyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	report, err := artifacts.RecomputeStorage(req.Context(), h.r.db, body.DryRun, func(ctx context.Context, tx *sql.Tx, rep artifacts.StorageRecomputeReport) error {
		return auditInstance(ctx, req, tx, "instance.artifact_storage_recomputed", "artifact_storage", "", "", map[string]any{
			"changed": rep.Changed, "workspaces_checked": rep.WorkspacesChecked, "workspaces_drifted": rep.WorkspacesDrifted,
			"used_bytes_before": rep.Instance.UsedBefore, "used_bytes_after": rep.Instance.UsedAfter,
			"reserved_bytes_before": rep.Instance.ReservedBefore, "reserved_bytes_after": rep.Instance.ReservedAfter,
		})
	})
	if err != nil {
		h.fail(w, req, "recompute artifact storage", err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
