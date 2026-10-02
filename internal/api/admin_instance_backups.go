package api

// Admin › Backups across workspaces. An instance admin reads the catalog of
// every workspace's bundles, pins the ones no retention rule may delete, and
// reads back every restore and dry run the server ran:
//
//	GET  /api/v1/admin/instance/backups/bundles        every catalogued bundle, with proof level, pin and recorded gaps
//	POST /api/v1/admin/instance/backups/bundles/pin    {path} — never deleted by retention
//	POST /api/v1/admin/instance/backups/bundles/unpin  {path}
//	GET  /api/v1/admin/instance/backups/restores       restore_reports, newest first
//
// Pin and unpin write their instance audit entry in the same transaction as
// the change: a pin that commits with no audit entry is not one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

type InstanceBackupsHandler struct {
	uploadBusy atomic.Bool
	db         *sql.DB
	logger     *slog.Logger

	// Whole-instance backup and recovery (admin_instance_backups_recovery.go).
	recoveryMu sync.RWMutex
	recovery   InstanceRecoveryConfig
	// windows hands a quiet window the backup service opened (quiescer)
	// to the instance executor of the same run, by run id.
	windowsMu sync.Mutex
	windows   map[string]*quiesce.Window
	// onDrill raises or clears the plan's drill incident when a drill is
	// recorded (the backup service's RecordDrillOutcome).
	onDrill func(ctx context.Context, planID, result, detail string)
}

func NewInstanceBackupsHandler(db *sql.DB, logger *slog.Logger) *InstanceBackupsHandler {
	return &InstanceBackupsHandler{db: db, logger: logger}
}

// instanceBackupBundle is one row of GET …/backups/bundles.
type instanceBackupBundle struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	FileName      string `json:"file_name"`
	Scope         string `json:"scope"`
	ScopeLevel    string `json:"scope_level"`
	Kind          string `json:"kind"`
	Slug          string `json:"slug"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	SizeBytes     int64  `json:"size_bytes"`
	PayloadSHA256 string `json:"payload_sha256"`
	Encrypted     bool   `json:"encrypted"`
	FormatVersion int    `json:"format_version"`
	PlanID        string `json:"plan_id"`
	RunID         string `json:"run_id"`
	Pinned        bool   `json:"pinned"`
	// ProofLevel: 1 checksum recorded, 2 contents checked, 3 test restore.
	ProofLevel     int     `json:"proof_level"`
	ProofCheckedAt *string `json:"proof_checked_at"`
	// DrillResult is ok | partial | failed, or "" when never drilled.
	DrillResult string          `json:"drill_result"`
	DrillAt     *string         `json:"drill_at"`
	DrillReport json.RawMessage `json:"drill_report"`
	// Incomplete is null when the gaps were never recorded (a bundle
	// catalogued before the column existed) and [] when there are none.
	Incomplete []backup.IncompleteItem `json:"incomplete"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// ListBundles is GET /api/v1/admin/instance/backups/bundles. Optional
// ?workspace=a,b (ids or slugs) and ?scope=workspace|crew narrow it.
func (h *InstanceBackupsHandler) ListBundles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ik := &InstanceKeeperHandler{db: h.db, logger: h.logger}
	all, err := ik.workspaces(ctx)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	byID := make(map[string]instanceWorkspaceRef, len(all))
	for _, ws := range all {
		byID[ws.ID] = ws
	}
	var only map[string]bool
	if raw := strings.TrimSpace(r.URL.Query().Get("workspace")); raw != "" {
		picked, err := pick(all, strings.Split(raw, ","))
		if err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		only = map[string]bool{}
		for _, ws := range picked {
			only[ws.ID] = true
		}
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	// Rows whose file is gone are pruned first, so the list never offers a
	// bundle that 404s. Best effort: a prune failure still lists.
	if _, err := backup.ReconcileCatalog(ctx, h.db, ""); err != nil {
		h.logger.Warn("instance backups: reconcile catalog", "error", err)
	}
	cat, err := backup.ListCatalog(ctx, h.db, "")
	if err != nil {
		h.fail(w, "list catalog", err)
		return
	}
	out := []instanceBackupBundle{}
	for _, e := range cat {
		if only != nil && !only[e.WorkspaceID] {
			continue
		}
		if scope != "" && e.Scope != scope {
			continue
		}
		ws := byID[e.WorkspaceID]
		out = append(out, instanceBackupBundle{
			ID: e.ID, Path: e.FilePath, FileName: filepath.Base(e.FilePath), Scope: e.Scope, ScopeLevel: e.ScopeLevel,
			Kind: e.Kind, Slug: e.Slug, WorkspaceID: e.WorkspaceID, WorkspaceName: ws.Name, WorkspaceSlug: ws.Slug,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339), CreatedBy: e.CreatedBy, SizeBytes: e.Size,
			PayloadSHA256: e.SHA256, Encrypted: e.Encrypted, FormatVersion: e.FormatVersion,
			PlanID: e.PlanID, RunID: e.RunID, Pinned: e.Pinned, ProofLevel: e.ProofLevel,
			ProofCheckedAt: rfc3339Ptr(e.ProofCheckedAt), DrillResult: e.DrillResult, DrillAt: rfc3339Ptr(e.DrillAt),
			DrillReport: e.DrillReport, Incomplete: e.Incomplete,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bundles": out})
}

type bundlePinRequest struct {
	Path string `json:"path"`
}

type bundlePinResponse struct {
	Path   string `json:"path"`
	Pinned bool   `json:"pinned"`
}

// Pin is POST /api/v1/admin/instance/backups/bundles/pin.
func (h *InstanceBackupsHandler) Pin(w http.ResponseWriter, r *http.Request) {
	h.setPinned(w, r, true)
}

// Unpin is POST /api/v1/admin/instance/backups/bundles/unpin.
func (h *InstanceBackupsHandler) Unpin(w http.ResponseWriter, r *http.Request) {
	h.setPinned(w, r, false)
}

func (h *InstanceBackupsHandler) setPinned(w http.ResponseWriter, r *http.Request, pinned bool) {
	ctx := r.Context()
	var req bundlePinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		replyError(w, http.StatusBadRequest, "path required")
		return
	}
	// The catalog is the authority: only a bundle the server catalogued can
	// be pinned, and the flag lives nowhere else — no filesystem access.
	entry, err := backup.GetCatalogEntry(ctx, h.db, req.Path)
	if errors.Is(err, backup.ErrCatalogEntryNotFound) {
		replyError(w, http.StatusNotFound, "no catalogued backup at that path")
		return
	}
	if err != nil {
		h.fail(w, "get catalog entry", err)
		return
	}
	if err := h.pinInTx(ctx, r, entry, pinned); err != nil {
		if errors.Is(err, backup.ErrCatalogEntryNotFound) {
			replyError(w, http.StatusNotFound, "no catalogued backup at that path")
			return
		}
		h.fail(w, "pin", err)
		return
	}
	writeJSON(w, http.StatusOK, bundlePinResponse{Path: entry.FilePath, Pinned: pinned})
}

func (h *InstanceBackupsHandler) pinInTx(ctx context.Context, r *http.Request, entry backup.CatalogEntry, pinned bool) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	action := "instance.backup_unpinned"
	if pinned {
		action = "instance.backup_pinned"
		err = backup.PinCatalogEntry(ctx, tx, entry.FilePath)
	} else {
		err = backup.UnpinCatalogEntry(ctx, tx, entry.FilePath)
	}
	if err != nil {
		return err
	}
	if err := auditInstance(ctx, r, tx, action, "backup", entry.FilePath, entry.WorkspaceID, map[string]any{
		"scope": entry.Scope, "slug": entry.Slug, "was_pinned": entry.Pinned,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// instanceRestoreReport is one row of GET …/backups/restores.
type instanceRestoreReport struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	ActorUserID string          `json:"actor_user_id"`
	ActorEmail  string          `json:"actor_email"`
	BundlePath  string          `json:"bundle_path"`
	Target      string          `json:"target"`
	Result      string          `json:"result"`
	Report      json.RawMessage `json:"report"`
	CreatedAt   string          `json:"created_at"`
}

// ListRestores is GET /api/v1/admin/instance/backups/restores?limit=N
// (default 100, max 500): every restore, dry run and drill, newest first,
// with the full report each one produced.
func (h *InstanceBackupsHandler) ListRestores(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			replyError(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}
	reports, err := backup.ListRestoreReports(ctx, h.db, limit)
	if err != nil {
		h.fail(w, "list restore reports", err)
		return
	}
	emails := map[string]string{}
	out := make([]instanceRestoreReport, 0, len(reports))
	for _, rep := range reports {
		email, seen := emails[rep.ActorUserID]
		if !seen && rep.ActorUserID != "" {
			_ = h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, rep.ActorUserID).Scan(&email)
			emails[rep.ActorUserID] = email
		}
		out = append(out, instanceRestoreReport{
			ID: rep.ID, Kind: rep.Kind, ActorUserID: rep.ActorUserID, ActorEmail: email, BundlePath: rep.BundlePath,
			Target: rep.Target, Result: rep.Result, Report: rep.Report, CreatedAt: rep.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"restores": out})
}

func (h *InstanceBackupsHandler) fail(w http.ResponseWriter, what string, err error) {
	h.logger.Error("instance backups: "+what, "error", err)
	replyError(w, http.StatusInternalServerError, "internal error")
}
