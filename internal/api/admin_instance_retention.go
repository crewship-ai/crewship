package api

// Admin › Data retention. An instance admin sees and sets how long every
// workspace keeps each kind of data:
//
//	GET /api/v1/admin/instance/retention?ws=<id|slug>&ws=…   every window of the chosen (or all) workspaces, the defaults and the fixed instance limits
//	PUT /api/v1/admin/instance/retention                     one, several or every existing workspace; dry_run previews
//	GET /api/v1/admin/instance/retention/defaults            what a workspace created later starts with
//	PUT /api/v1/admin/instance/retention/defaults            change those defaults; touches no existing workspace; dry_run previews, expect_preview confirms
//
// A save follows the Keeper governance pattern (admin_instance_keeper.go): one
// transaction for every workspace or none and an instance audit entry per
// changed workspace. Every change reports, per workspace and per window, how
// many rows the next sweep would delete under the new window, so the console
// can say so before anything is saved.
//
// "All workspaces" is only a selection: it means every workspace that exists
// now and never changes the defaults. The defaults a new workspace starts from
// are a separate operation with its own route, its own confirmation and its
// own audit entry, because they touch no existing data.
//
// The windows themselves, their storage and their sweeps live in
// internal/retention.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/retention"
)

type InstanceRetentionHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewInstanceRetentionHandler(db *sql.DB, logger *slog.Logger) *InstanceRetentionHandler {
	return &InstanceRetentionHandler{db: db, logger: logger}
}

type instanceRetentionRow struct {
	instanceWorkspaceRef
	Windows retention.Windows `json:"windows"`
}

type instanceRetentionList struct {
	Workspaces []instanceRetentionRow `json:"workspaces"`
	// Defaults is what a workspace created now starts with.
	Defaults retention.Windows `json:"defaults"`
	// DefaultsConfigured names the keys an administrator saved for all
	// workspaces; the other defaults are the product's.
	DefaultsConfigured []retention.Key `json:"defaults_configured"`
	// Keys describes every window: label, product default, bounds, whether it
	// can be forever, and what its sweep does.
	Keys []retention.KeyInfo `json:"keys"`
	// Housekeeping is the fixed instance limits, read-only.
	Housekeeping []retention.HousekeepingItem `json:"housekeeping"`
}

// Get is GET /api/v1/admin/instance/retention. ws may repeat and may carry
// several ids or slugs separated by commas; without it, every workspace.
func (h *InstanceRetentionHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	every, err := instanceWorkspaces(ctx, h.db)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	targets := every
	if q.Has("ws") {
		var refs []string
		for _, v := range q["ws"] {
			refs = append(refs, strings.Split(v, ",")...)
		}
		if targets, err = pick(every, refs); err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
	}
	out := instanceRetentionList{
		Workspaces:   make([]instanceRetentionRow, 0, len(targets)),
		Keys:         retention.Keys,
		Housekeeping: retention.Housekeeping(),
	}
	for _, ws := range targets {
		win, err := retention.Load(ctx, h.db, ws.ID)
		if err != nil {
			h.fail(w, "load", err)
			return
		}
		out.Workspaces = append(out.Workspaces, instanceRetentionRow{instanceWorkspaceRef: ws, Windows: win})
	}
	if out.Defaults, out.DefaultsConfigured, err = retention.Defaults(ctx, h.db); err != nil {
		h.fail(w, "defaults", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type instanceRetentionChange struct {
	Key  retention.Key `json:"key"`
	From *int          `json:"from"`
	To   *int          `json:"to"`
	// RowsAffectedNextSweep is how many rows the next sweep deletes under To:
	// counted, not deleted. Backups taken before still hold them.
	RowsAffectedNextSweep int64 `json:"rows_affected_next_sweep"`
}

type instanceRetentionWorkspaceChange struct {
	instanceWorkspaceRef
	// Changes is empty when the workspace already has these windows; such a
	// workspace is left alone.
	Changes []instanceRetentionChange `json:"changes"`
}

type instanceRetentionPutResponse struct {
	Applied bool `json:"applied"`
	DryRun  bool `json:"dry_run"`
	// Changed is how many workspaces the save changes (or, on a dry run, would).
	Changed int `json:"changed"`
	// RowsAffectedNextSweep sums every change's count.
	RowsAffectedNextSweep int64                              `json:"rows_affected_next_sweep"`
	Workspaces            []instanceRetentionWorkspaceChange `json:"workspaces"`
	// PreviewID fingerprints the targets and every from → to (not the row
	// counts, which move as data ages). Send it back as expect_preview.
	PreviewID string `json:"preview_id"`
}

// Put is PUT /api/v1/admin/instance/retention.
//
//	{ "workspace_ids": ["id-or-slug", …] | null, "windows": { "inbox_days": 90, "chats_days": null }, "dry_run": true }
//
// workspace_ids must be present: null means every existing workspace, a list
// names the targets. Neither changes the defaults for new workspaces (see
// PutDefaults). windows carries only the keys being
// changed; null is forever where the window allows it.
func (h *InstanceRetentionHandler) Put(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Decoded twice: once to tell a missing workspace_ids from an explicit
	// null (all), once into the typed body.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var body struct {
		WorkspaceIDs  []string                   `json:"workspace_ids"`
		Windows       map[string]json.RawMessage `json:"windows"`
		DryRun        bool                       `json:"dry_run"`
		ExpectPreview string                     `json:"expect_preview"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	idsRaw, present := raw["workspace_ids"]
	if !present {
		replyError(w, http.StatusBadRequest, "send workspace_ids: a list of workspaces, or null for all")
		return
	}
	all := strings.TrimSpace(string(idsRaw)) == "null"
	if !all && len(body.WorkspaceIDs) == 0 {
		replyError(w, http.StatusBadRequest, "workspace_ids is empty: name the workspaces, or send null for all")
		return
	}
	patch, err := retention.ParsePatch(body.Windows)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	every, err := instanceWorkspaces(ctx, h.db)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	targets := every
	if !all {
		if targets, err = pick(every, body.WorkspaceIDs); err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		if len(targets) == 0 {
			replyError(w, http.StatusBadRequest, "workspace_ids is empty: name the workspaces, or send null for all")
			return
		}
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	resp := instanceRetentionPutResponse{DryRun: body.DryRun, Workspaces: make([]instanceRetentionWorkspaceChange, 0, len(targets))}
	for _, ws := range targets {
		cur, err := retention.Load(ctx, tx, ws.ID)
		if err != nil {
			h.fail(w, "load", err)
			return
		}
		c := instanceRetentionWorkspaceChange{instanceWorkspaceRef: ws, Changes: []instanceRetentionChange{}}
		for _, info := range retention.Keys {
			to, ok := patch[info.Key]
			if !ok || retention.Equal(cur[info.Key], to) {
				continue
			}
			n, err := retention.Count(ctx, tx, ws.ID, info.Key, to, now)
			if err != nil {
				h.fail(w, "count", err)
				return
			}
			c.Changes = append(c.Changes, instanceRetentionChange{Key: info.Key, From: cur[info.Key], To: to, RowsAffectedNextSweep: n})
			resp.RowsAffectedNextSweep += n
		}
		if len(c.Changes) > 0 {
			resp.Changed++
		}
		resp.Workspaces = append(resp.Workspaces, c)
	}
	resp.PreviewID = retentionPreviewID(resp)
	if body.DryRun {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if body.ExpectPreview != "" && body.ExpectPreview != resp.PreviewID {
		replyError(w, http.StatusConflict, "the windows or the workspaces changed since the preview; review the changes again")
		return
	}

	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	for _, c := range resp.Workspaces {
		if len(c.Changes) == 0 {
			continue
		}
		for _, ch := range c.Changes {
			if err := retention.Store(ctx, tx, c.ID, ch.Key, ch.To, actor, now); err != nil {
				var ve retention.ValidationError
				if errors.As(err, &ve) {
					replyError(w, http.StatusBadRequest, ve.Error())
					return
				}
				h.fail(w, "store", err)
				return
			}
		}
		if err := auditInstance(ctx, r, tx, "instance.retention_updated", "workspace", c.ID, c.ID, map[string]any{
			"workspace_name": c.Name,
			"changes":        c.Changes,
			"targets":        len(targets),
			"all":            all,
		}); err != nil {
			h.fail(w, "audit", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	resp.Applied = true
	writeJSON(w, http.StatusOK, resp)
}

// retentionPreviewID fingerprints a planned save: the targets and every
// from → to.
func retentionPreviewID(resp instanceRetentionPutResponse) string {
	type change struct {
		Key  retention.Key `json:"key"`
		From *int          `json:"from"`
		To   *int          `json:"to"`
	}
	type ws struct {
		ID      string   `json:"id"`
		Changes []change `json:"changes"`
	}
	plan := struct {
		Workspaces []ws `json:"workspaces"`
	}{}
	for _, w := range resp.Workspaces {
		x := ws{ID: w.ID, Changes: []change{}}
		for _, c := range w.Changes {
			x.Changes = append(x.Changes, change{Key: c.Key, From: c.From, To: c.To})
		}
		plan.Workspaces = append(plan.Workspaces, x)
	}
	b, _ := json.Marshal(plan)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

type instanceRetentionDefaults struct {
	// Defaults is what a workspace created now starts with, every window.
	Defaults retention.Windows `json:"defaults"`
	// Configured names the windows an administrator set; the others are the
	// product defaults.
	Configured []retention.Key `json:"configured"`
}

// GetDefaults is GET /api/v1/admin/instance/retention/defaults.
func (h *InstanceRetentionHandler) GetDefaults(w http.ResponseWriter, r *http.Request) {
	d, configured, err := retention.Defaults(r.Context(), h.db)
	if err != nil {
		h.fail(w, "defaults", err)
		return
	}
	writeJSON(w, http.StatusOK, instanceRetentionDefaults{Defaults: d, Configured: configured})
}

type instanceRetentionDefaultsChange struct {
	Key  retention.Key `json:"key"`
	From *int          `json:"from"`
	To   *int          `json:"to"`
}

type instanceRetentionDefaultsPutResponse struct {
	Applied bool `json:"applied"`
	DryRun  bool `json:"dry_run"`
	// AffectsExisting is always false: defaults are what a workspace created
	// later starts with. No existing workspace changes and no row is deleted,
	// which is why there are no row counts here.
	AffectsExisting bool                              `json:"affects_existing"`
	Changes         []instanceRetentionDefaultsChange `json:"changes"`
	// Defaults is every window as a new workspace would start after the save
	// (or, on a dry run, would).
	Defaults retention.Windows `json:"defaults"`
	// PreviewID fingerprints every window's current default and every
	// from → to. Send it back as expect_preview.
	PreviewID string `json:"preview_id"`
}

// PutDefaults is PUT /api/v1/admin/instance/retention/defaults:
//
//	{ "windows": { "inbox_days": 90, "chats_days": null }, "dry_run": true, "expect_preview": "…" }
//
// Only the windows sent change. A window already at the value is not a change
// and records nothing. expect_preview, the preview_id of the dry run the admin
// confirmed, holds the save to that preview: a default changed since is a 409
// and nothing is written.
func (h *InstanceRetentionHandler) PutDefaults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Windows       map[string]json.RawMessage `json:"windows"`
		DryRun        bool                       `json:"dry_run"`
		ExpectPreview string                     `json:"expect_preview"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	patch, err := retention.ParsePatch(body.Windows)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	cur, _, err := retention.Defaults(ctx, tx)
	if err != nil {
		h.fail(w, "defaults", err)
		return
	}
	resp := instanceRetentionDefaultsPutResponse{DryRun: body.DryRun, Changes: []instanceRetentionDefaultsChange{}, Defaults: retention.Windows{}}
	changed := retention.Windows{}
	for _, info := range retention.Keys {
		resp.Defaults[info.Key] = cur[info.Key]
		to, ok := patch[info.Key]
		if !ok || retention.Equal(cur[info.Key], to) {
			continue
		}
		resp.Changes = append(resp.Changes, instanceRetentionDefaultsChange{Key: info.Key, From: cur[info.Key], To: to})
		resp.Defaults[info.Key] = to
		changed[info.Key] = to
	}
	resp.PreviewID = retentionDefaultsPreviewID(cur, resp.Changes)
	if body.DryRun {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if body.ExpectPreview != "" && body.ExpectPreview != resp.PreviewID {
		replyError(w, http.StatusConflict, "the defaults changed since the preview; review the changes again")
		return
	}
	if len(changed) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err := retention.MergeDefaults(ctx, tx, changed, time.Now()); err != nil {
		h.fail(w, "set defaults", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.retention_defaults_updated", "instance", "", "", map[string]any{
		"changes": resp.Changes,
	}); err != nil {
		h.fail(w, "audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	resp.Applied = true
	writeJSON(w, http.StatusOK, resp)
}

// retentionDefaultsPreviewID fingerprints a planned defaults save: every
// window's current default (so a change to any of them, not only the ones
// being saved, invalidates the preview) and every from → to.
func retentionDefaultsPreviewID(cur retention.Windows, changes []instanceRetentionDefaultsChange) string {
	type window struct {
		Key  retention.Key `json:"key"`
		Days *int          `json:"days"`
	}
	plan := struct {
		Current []window                          `json:"current"`
		Changes []instanceRetentionDefaultsChange `json:"changes"`
	}{Current: []window{}, Changes: changes}
	for _, info := range retention.Keys {
		plan.Current = append(plan.Current, window{Key: info.Key, Days: cur[info.Key]})
	}
	b, _ := json.Marshal(plan)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func (h *InstanceRetentionHandler) fail(w http.ResponseWriter, what string, err error) {
	h.logger.Error("instance retention: "+what, "error", err)
	replyError(w, http.StatusInternalServerError, "internal error")
}
