package api

// Admin › Backups: whole-instance backup and recovery (Track B).
//
//
// Runs (POST …/backups/run, GET …/backups/run/{runId}) belong to the backup
// service (internal/backupplan, admin_instance_backup_plans.go); the instance
// bundle it writes comes from instanceExecutor (admin_instance_backup_bridge.go).
//
//	GET  /api/v1/admin/instance/backups/vault-keys            key versions on this server, envelopes per version, recovery kit state
//	PUT  /api/v1/admin/instance/backups/settings/recovery-kit {enabled}
//	POST /api/v1/admin/instance/backups/bundles/check         proof level 2: decrypt, read every section, compare with the manifest
//	POST /api/v1/admin/instance/backups/restore/checks        space, format, runtime, unsafe settings, conflicts
//	POST /api/v1/admin/instance/backups/drills                record a test restore run by `crewship backup drill --post`
//	GET  /api/v1/admin/instance/backups/drills                every recorded drill
//	GET  /api/v1/admin/instance/holds                         automations an instance restore left held
//	POST /api/v1/admin/instance/holds/resume                  {key}
//
// Every change writes its instance audit entry in the transaction that makes
// it. Keys given to a check are used for that request only and never stored
// or logged.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// InstanceRecoveryConfig is what the instance backup needs from the router:
// where the file stores are and how to reach crew containers.
type InstanceRecoveryConfig struct {
	ServiceSnapshots  backup.ServiceSnapshotRuntime
	Paths             backup.InstancePaths
	OutputDir         string // "" = the default backups directory
	DataDir           string // for free-space checks
	DockerOps         backup.DockerOps
	DockerPing        func(ctx context.Context) error
	CrewContainerName func(id, slug string) string
	CrewshipVersion   string
	// Quiesce is the quiet-window controller (nil: the process-wide one the
	// router's write gate reads).
	Quiesce *quiesce.Controller
}

// SetRecovery wires the instance backup. Without it run/check/restore-checks
// still answer, against the default backups directory and without Docker.
func (h *InstanceBackupsHandler) SetRecovery(cfg InstanceRecoveryConfig) {
	h.recoveryMu.Lock()
	h.recovery = cfg
	h.recoveryMu.Unlock()
}

// recoveryConfig is the configuration a run reads when it starts.
func (h *InstanceBackupsHandler) recoveryConfig() InstanceRecoveryConfig {
	h.recoveryMu.RLock()
	defer h.recoveryMu.RUnlock()
	return h.recovery
}

func decodeInstanceBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func actorUserID(r *http.Request) (string, string) {
	if u := UserFromContext(r.Context()); u != nil {
		return u.ID, u.Email
	}
	return "", ""
}

// ── Vault keys and the recovery kit ─────────────────────────────────────────

type vaultKeyVersion struct {
	Version string `json:"version"`
	Env     string `json:"env"`
	// Active: new envelopes are minted with this version.
	Active bool `json:"active"`
	// Envelopes sealed with this version in the database.
	Envelopes int `json:"envelopes"`
	// Present: this server has a key for the version (its own variable, or
	// the ENCRYPTION_KEY fallback Decrypt applies).
	Present bool `json:"present"`
}

type vaultKeysResponse struct {
	Versions    []vaultKeyVersion `json:"versions"`
	RecoveryKit struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"recovery_kit"`
}

// VaultKeys is GET /api/v1/admin/instance/backups/vault-keys. Counts only —
// no key material and no plaintext leave the server.
func (h *InstanceBackupsHandler) VaultKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scan, err := backup.ScanVaultEnvelopes(ctx, h.db)
	if err != nil {
		h.fail(w, "scan vault envelopes", err)
		return
	}
	enabled, err := backup.RecoveryKitEnabled(ctx, h.db)
	if err != nil {
		h.fail(w, "read recovery kit setting", err)
		return
	}
	current, _ := encryption.CurrentKeyVersion()
	seen := map[string]bool{}
	var versions []string
	for _, v := range encryption.ConfiguredKeyVersions() {
		seen[v] = true
		versions = append(versions, v)
	}
	for v := range scan.Versions {
		if !seen[v] {
			seen[v] = true
			versions = append(versions, v)
		}
	}
	encryption.SortVersions(versions)
	resp := vaultKeysResponse{Versions: []vaultKeyVersion{}}
	available := true
	for _, v := range versions {
		_, rerr := encryption.ResolveKey(v)
		row := vaultKeyVersion{Version: v, Env: encryption.KeyEnvVar(v), Active: v == current, Envelopes: scan.Versions[v], Present: rerr == nil}
		if row.Envelopes > 0 && !row.Present {
			available = false
		}
		resp.Versions = append(resp.Versions, row)
	}
	resp.RecoveryKit.Available = available
	resp.RecoveryKit.Enabled = enabled
	writeJSON(w, http.StatusOK, resp)
}

type recoveryKitRequest struct {
	Enabled *bool `json:"enabled"`
}

type recoveryKitResponse struct {
	Enabled bool `json:"enabled"`
}

// SetRecoveryKit is PUT /api/v1/admin/instance/backups/settings/recovery-kit.
func (h *InstanceBackupsHandler) SetRecoveryKit(w http.ResponseWriter, r *http.Request) {
	var req recoveryKitRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		replyError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	ctx := r.Context()
	was, err := backup.RecoveryKitEnabled(ctx, h.db)
	if err != nil {
		h.fail(w, "read recovery kit setting", err)
		return
	}
	userID, _ := actorUserID(r)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backup.SetRecoveryKitEnabled(ctx, tx, *req.Enabled, userID, time.Now()); err != nil {
		h.fail(w, "set recovery kit", err)
		return
	}
	action := "instance.backup_recovery_kit_disabled"
	if *req.Enabled {
		action = "instance.backup_recovery_kit_enabled"
	}
	if err := auditInstance(ctx, r, tx, action, "backup_settings", "recovery_kit", "", map[string]any{"was": was, "now": *req.Enabled}); err != nil {
		h.fail(w, "audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryKitResponse{Enabled: *req.Enabled})
}

// ── Contents check (proof 2) ────────────────────────────────────────────────

type bundleKeyRequest struct {
	Path       string `json:"path"`
	Identity   string `json:"identity"`
	Passphrase string `json:"passphrase"`
}

// parseKey turns a request's key into what the backup package takes. The
// identity text is parsed in memory and dropped with the request.
func (req bundleKeyRequest) parseKey() ([]age.Identity, string, error) {
	if req.Identity != "" && req.Passphrase != "" {
		return nil, "", errors.New("give identity or passphrase, not both")
	}
	if req.Identity != "" {
		ids, err := age.ParseIdentities(strings.NewReader(req.Identity))
		if err != nil {
			return nil, "", errors.New("identity is not an age secret key (AGE-SECRET-KEY-1…)")
		}
		return ids, "", nil
	}
	return nil, req.Passphrase, nil
}

type bundleCheckResponse struct {
	OK         bool                    `json:"ok"`
	ProofLevel int                     `json:"proof_level"`
	Detail     string                  `json:"detail"`
	Problems   []string                `json:"problems"`
	Absent     []backup.IncompleteItem `json:"absent"`
	Entries    int                     `json:"entries"`
	Tables     int                     `json:"tables"`
	Files      int                     `json:"files"`
}

// CheckBundle is POST /api/v1/admin/instance/backups/bundles/check.
func (h *InstanceBackupsHandler) CheckBundle(w http.ResponseWriter, r *http.Request) {
	var req bundleKeyRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	ctx := r.Context()
	entry, ok := h.catalogued(w, ctx, req.Path)
	if !ok {
		return
	}
	ids, pass, err := req.parseKey()
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if entry.Encrypted && len(ids) == 0 && pass == "" {
		replyError(w, http.StatusBadRequest, "a contents check decrypts the bundle: give identity or passphrase")
		return
	}
	res, err := backup.CheckBundleContents(ctx, entry.FilePath, ids, pass)
	if err != nil {
		if errors.Is(err, backup.ErrDecryption) {
			replyError(w, http.StatusUnprocessableEntity, "that key does not open this backup")
			return
		}
		h.fail(w, "check bundle", err)
		return
	}
	level := entry.ProofLevel
	switch {
	case res.OK && level < backup.ProofContents:
		level = backup.ProofContents
	case !res.OK && level == backup.ProofContents:
		level = backup.ProofChecksum // a failed check disproves "contents checked"
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if level != entry.ProofLevel || res.OK {
		if err := backup.SetCatalogProof(ctx, tx, entry.FilePath, level, time.Now()); err != nil {
			h.fail(w, "set proof", err)
			return
		}
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_contents_checked", "backup", entry.FilePath, entry.WorkspaceID, map[string]any{
		"ok": res.OK, "proof_level": level, "problems": len(res.Problems),
	}); err != nil {
		h.fail(w, "audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	writeJSON(w, http.StatusOK, bundleCheckResponse{OK: res.OK, ProofLevel: level, Detail: res.Detail, Problems: res.Problems,
		Absent: res.Absent, Entries: res.Entries, Tables: res.Tables, Files: res.Files})
}

// catalogued resolves a request path to its catalog entry: the catalog is the
// authority on which files are backups, so no path outside it is ever opened.
func (h *InstanceBackupsHandler) catalogued(w http.ResponseWriter, ctx context.Context, p string) (backup.CatalogEntry, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		replyError(w, http.StatusBadRequest, "path required")
		return backup.CatalogEntry{}, false
	}
	entry, err := backup.GetCatalogEntry(ctx, h.db, p)
	if errors.Is(err, backup.ErrCatalogEntryNotFound) {
		replyError(w, http.StatusNotFound, "no catalogued backup at that path")
		return entry, false
	}
	if err != nil {
		h.fail(w, "get catalog entry", err)
		return entry, false
	}
	if _, err := os.Stat(entry.FilePath); err != nil {
		replyError(w, http.StatusNotFound, "the backup file is no longer on disk")
		return entry, false
	}
	return entry, true
}

// ── Restore checks ──────────────────────────────────────────────────────────

type restoreChecksRequest struct {
	bundleKeyRequest
	Target string `json:"target"`
}

// RestoreChecks is POST /api/v1/admin/instance/backups/restore/checks.
func (h *InstanceBackupsHandler) RestoreChecks(w http.ResponseWriter, r *http.Request) {
	var req restoreChecksRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	if !backup.ValidRestoreTarget(req.Target) {
		replyError(w, http.StatusBadRequest, "target must be empty_server, isolated, replace, new_workspace or crew")
		return
	}
	ctx := r.Context()
	entry, ok := h.catalogued(w, ctx, req.Path)
	if !ok {
		return
	}
	m, err := backup.Inspect(ctx, entry.FilePath)
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, "the bundle cannot be read: "+err.Error())
		return
	}
	in := backup.RestoreCheckInput{Manifest: m, BundleSize: entry.Size, Target: req.Target}
	cfg := h.recoveryConfig()
	dataDir := cfg.DataDir
	if dataDir == "" {
		if dd, err := database.ResolveDefaultDataDir(); err == nil {
			dataDir = dd.Root
		}
	}
	if st, err := diskusage.Usage(existingAncestor(dataDir)); err == nil {
		in.FreeBytes = int64(st.FreeBytes)
	}
	switch {
	case cfg.DockerPing != nil:
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		in.DockerErr = cfg.DockerPing(pctx)
		cancel()
	default:
		in.DockerErr = errors.New("no Docker client on this server")
	}
	// The daemon's platform is what a restored environment runs on; the
	// server binary's own GOARCH is only the fallback.
	if eo, ok := cfg.DockerOps.(backup.EnvironmentOps); ok && in.DockerErr == nil && len(m.Contents.Environments) > 0 {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if ri, err := eo.RuntimeInfo(rctx); err == nil {
			in.HostPlatform, in.DockerVersion = ri.Platform(), ri.DockerVersion
		}
		cancel()
	}
	// Layers are available when the bundle carries them inline or the
	// store beside it still holds them.
	envStore := backup.EnvironmentStoreFor(filepath.Dir(entry.FilePath))
	inline := map[string]bool{}
	for _, e := range m.Contents.Environments {
		if e.Inline {
			for _, d := range e.Blobs {
				inline[d] = true
			}
		}
	}
	in.HasBlob = func(d string) bool { return inline[d] || envStore.Has(d) }
	ids, pass, err := req.parseKey()
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(ids) > 0 || pass != "" {
		unsafe, err := backup.ScanUnsafeSettings(ctx, entry.FilePath, ids, pass)
		if err != nil {
			if errors.Is(err, backup.ErrDecryption) {
				replyError(w, http.StatusUnprocessableEntity, "that key does not open this backup")
				return
			}
			h.fail(w, "scan unsafe settings", err)
			return
		}
		in.Unsafe, in.UnsafeChecked = unsafe, true
	}
	in.WorkspaceExists = func(s string) bool {
		var n int
		_ = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE (id = ? OR slug = ?) AND deleted_at IS NULL`, s, s).Scan(&n)
		return n > 0
	}
	writeJSON(w, http.StatusOK, backup.EvaluateRestoreChecks(in))
}

func existingAncestor(p string) string {
	for p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return "/"
}

// ── Drills ──────────────────────────────────────────────────────────────────

type drillRecordRequest struct {
	Path   string          `json:"path"`
	SHA256 string          `json:"sha256"`
	Result string          `json:"result"`
	Report json.RawMessage `json:"report"`
}

type drillRecordResponse struct {
	Path        string `json:"path"`
	ProofLevel  int    `json:"proof_level"`
	DrillResult string `json:"drill_result"`
	DrillAt     string `json:"drill_at"`
	ReportID    string `json:"report_id"`
}

// RecordDrill is POST /api/v1/admin/instance/backups/drills.
func (h *InstanceBackupsHandler) RecordDrill(w http.ResponseWriter, r *http.Request) {
	var req drillRecordRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	switch req.Result {
	case backup.RestoreResultOK, backup.RestoreResultPartial, backup.RestoreResultFailed:
	default:
		replyError(w, http.StatusBadRequest, "result must be ok, partial or failed")
		return
	}
	if len(req.Report) == 0 {
		req.Report = json.RawMessage("{}")
	}
	if !json.Valid(req.Report) {
		replyError(w, http.StatusBadRequest, "report must be JSON")
		return
	}
	ctx := r.Context()
	entry, err := backup.GetCatalogEntry(ctx, h.db, strings.TrimSpace(req.Path))
	if errors.Is(err, backup.ErrCatalogEntryNotFound) {
		replyError(w, http.StatusNotFound, "no catalogued backup at that path")
		return
	}
	if err != nil {
		h.fail(w, "get catalog entry", err)
		return
	}
	if req.SHA256 == "" || req.SHA256 != entry.SHA256 {
		replyError(w, http.StatusConflict, "the drilled bundle's checksum does not match the catalogued one at that path")
		return
	}
	now := time.Now().UTC()
	userID, _ := actorUserID(r)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backup.SetCatalogDrill(ctx, tx, entry.FilePath, req.Result, req.Report, now); err != nil {
		h.fail(w, "set drill", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_drill_recorded", "backup", entry.FilePath, entry.WorkspaceID, map[string]any{"result": req.Result}); err != nil {
		h.fail(w, "audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	rep, err := backup.RecordRestoreReport(ctx, h.db, backup.RestoreReport{
		Kind: backup.RestoreKindDrill, ActorUserID: userID, BundlePath: entry.FilePath, Target: backup.TargetIsolated,
		Result: req.Result, Report: req.Report, CreatedAt: now,
	})
	if err != nil {
		h.fail(w, "record drill report", err)
		return
	}
	if h.onDrill != nil {
		h.onDrill(ctx, entry.PlanID, req.Result, fmt.Sprintf("Drill of %s: %s.", filepath.Base(entry.FilePath), req.Result))
	}
	updated, _ := backup.GetCatalogEntry(ctx, h.db, entry.FilePath)
	writeJSON(w, http.StatusCreated, drillRecordResponse{Path: entry.FilePath, ProofLevel: updated.ProofLevel,
		DrillResult: req.Result, DrillAt: now.Format(time.RFC3339), ReportID: rep.ID})
}

// drillRecord is one row of GET …/drills (RestoreRecord in the UI model).
type drillRecord struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Actor       string `json:"actor"`
	BundlePath  string `json:"bundle_path"`
	SourceScope string `json:"source_scope"`
	SourceName  string `json:"source_name"`
	SourceDate  string `json:"source_date"`
	Target      string `json:"target"`
	Result      string `json:"result"`
	Warnings    int    `json:"warnings"`
	CreatedAt   string `json:"created_at"`
}

// ListDrills is GET /api/v1/admin/instance/backups/drills: every drill,
// newest first, as a bare array.
func (h *InstanceBackupsHandler) ListDrills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reports, err := backup.ListRestoreReports(ctx, h.db, 500)
	if err != nil {
		h.fail(w, "list restore reports", err)
		return
	}
	out := []drillRecord{}
	emails := map[string]string{}
	for _, rep := range reports {
		if rep.Kind != backup.RestoreKindDrill {
			continue
		}
		actor, seen := emails[rep.ActorUserID]
		if !seen && rep.ActorUserID != "" {
			_ = h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, rep.ActorUserID).Scan(&actor)
			emails[rep.ActorUserID] = actor
		}
		if actor == "" {
			actor = rep.ActorUserID
		}
		rec := drillRecord{ID: rep.ID, Kind: rep.Kind, Actor: actor, BundlePath: rep.BundlePath, Target: rep.Target,
			Result: rep.Result, Warnings: drillWarnings(rep.Report), CreatedAt: rep.CreatedAt.UTC().Format(time.RFC3339),
			SourceScope: "workspaces", SourceName: filepath.Base(rep.BundlePath)}
		if e, err := backup.GetCatalogEntry(ctx, h.db, rep.BundlePath); err == nil {
			rec.SourceDate = e.CreatedAt.UTC().Format(time.RFC3339)
			rec.SourceName = e.Slug
			if e.Scope == string(backup.ScopeInstance) {
				rec.SourceScope, rec.SourceName = "instance", "Whole instance"
			}
		}
		out = append(out, rec)
	}
	writeJSON(w, http.StatusOK, out)
}

// drillWarnings counts the checks in a drill report that did not pass, plus
// the gaps its restore recorded.
func drillWarnings(report json.RawMessage) int {
	var rep struct {
		Checks []struct {
			Status string `json:"status"`
		} `json:"checks"`
		Restore *struct {
			Incomplete []json.RawMessage `json:"incomplete"`
		} `json:"restore"`
	}
	if json.Unmarshal(report, &rep) != nil {
		return 0
	}
	n := 0
	for _, c := range rep.Checks {
		if c.Status != "ok" {
			n++
		}
	}
	if rep.Restore != nil {
		n += len(rep.Restore.Incomplete)
	}
	return n
}

// ── Holds ───────────────────────────────────────────────────────────────────

type instanceHold struct {
	Key       string `json:"key"`
	Reason    string `json:"reason"`
	Count     int    `json:"count"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

// ListHolds is GET /api/v1/admin/instance/holds: the rows in instance_holds,
// as a bare array.
func (h *InstanceBackupsHandler) ListHolds(w http.ResponseWriter, r *http.Request) {
	holds, err := quiesce.ReadHolds(r.Context(), h.db)
	if err != nil {
		h.fail(w, "read holds", err)
		return
	}
	out := make([]instanceHold, 0, len(holds))
	for _, x := range holds {
		out = append(out, instanceHold{Key: x.Key, Reason: x.Reason, Count: x.Count, Detail: x.Detail, CreatedAt: x.CreatedAt.Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, out)
}

type resumeHoldRequest struct {
	Key string `json:"key"`
}

type resumeHoldResponse struct {
	Key     string `json:"key"`
	Resumed bool   `json:"resumed"`
}

// ResumeHold is POST /api/v1/admin/instance/holds/resume.
func (h *InstanceBackupsHandler) ResumeHold(w http.ResponseWriter, r *http.Request) {
	var req resumeHoldRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if !quiesce.ValidHoldKey(req.Key) {
		replyError(w, http.StatusBadRequest, "key must be routines, webhooks, queue or all")
		return
	}
	ctx := r.Context()
	holds, err := quiesce.ReadHolds(ctx, h.db)
	if err != nil {
		h.fail(w, "read holds", err)
		return
	}
	var found *quiesce.Hold
	for i := range holds {
		if holds[i].Key == req.Key || req.Key == quiesce.HoldAll {
			found = &holds[i]
			break
		}
	}
	if found == nil {
		replyError(w, http.StatusNotFound, "that automation is not held")
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteHold(ctx, tx, req.Key); err != nil {
		h.fail(w, "resume", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.hold_resumed", "instance_hold", req.Key, "", map[string]any{
		"count": found.Count, "detail": found.Detail, "held_since": found.CreatedAt.Format(time.RFC3339),
	}); err != nil {
		h.fail(w, "audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	quiesce.DefaultHolds().Forget(req.Key)
	writeJSON(w, http.StatusOK, resumeHoldResponse{Key: req.Key, Resumed: true})
}

func deleteHold(ctx context.Context, tx *sql.Tx, key string) error {
	if key == quiesce.HoldAll {
		_, err := tx.ExecContext(ctx, `DELETE FROM instance_holds`)
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM instance_holds WHERE key = ?`, key)
	return err
}

// SetInstanceRecovery replaces the instance backup's configuration (tests and
// embedders that keep their stores somewhere else).
func (r *Router) SetInstanceRecovery(cfg InstanceRecoveryConfig) {
	if r.instanceBackups != nil {
		r.instanceBackups.SetRecovery(cfg)
	}
}

// instanceRecoveryConfig gathers the file stores and the Docker wiring the
// instance backup needs from what the router was built with.
func (r *Router) instanceRecoveryConfig() InstanceRecoveryConfig {
	cfg := InstanceRecoveryConfig{
		Paths: backup.InstancePaths{
			Output:       r.storagePath,
			PageProjects: r.pageProjectsPath,
		},
		CrewshipVersion: os.Getenv("CREWSHIP_VERSION"),
	}
	if r.memoryVersionsBlobRoot != "" {
		cfg.Paths.Memory = filepath.Dir(r.memoryVersionsBlobRoot)
	}
	if dd, err := database.ResolveDefaultDataDir(); err == nil {
		cfg.DataDir = dd.Root
		cfg.Paths.Chats = dd.ChatsDir()
		cfg.Paths.Skills = dd.SkillsDir()
	}
	if r.dockerClient != nil {
		cli := r.dockerClient
		cfg.DockerOps = &backup.MobyDockerOps{Client: cli}
		ops := cfg.DockerOps
		// Reachability through a call the backup package already makes (a
		// container inspect answers "no such container" when the daemon is
		// up), so the Docker API surface this server needs does not grow.
		cfg.DockerPing = func(ctx context.Context) error {
			_, err := ops.ContainerExists(ctx, "crewship-restore-checks-probe")
			return err
		}
	}
	if r.keeperContainer != nil {
		cfg.CrewContainerName = r.keeperContainer.CrewContainerName
		cfg.ServiceSnapshots, _ = r.keeperContainer.(backup.ServiceSnapshotRuntime)
	}
	return cfg
}
