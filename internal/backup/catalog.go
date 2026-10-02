package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Catalog kinds (backup_catalog.kind).
const (
	KindFull         = "full"
	KindCustom       = "custom"
	KindEnvironments = "environments"
	KindInstance     = "instance"
)

// Proof levels stored per bundle in backup_catalog.proof_level. Never
// collapsed into one "restorable" flag: each says exactly what was shown.
const (
	// ProofChecksum: the bundle was written and its payload checksum is
	// recorded. Every new bundle starts here.
	ProofChecksum = 1
	// ProofContents: decrypted, every section read, row counts and hashes
	// matched the manifest.
	ProofContents = 2
	// ProofRestore: restored in isolation (a drill); drill_result says how
	// that went.
	ProofRestore = 3
)

// ErrCatalogEntryNotFound is returned by the per-path catalog helpers when
// no backup_catalog row has that file_path.
var ErrCatalogEntryNotFound = errors.New("backup: no catalog entry for that bundle")

// CatalogEntry is one row in the backup_catalog table — a pointer to
// a bundle on disk with the minimal metadata the admin list view
// needs. Fetching these avoids re-parsing every manifest on each page
// load, which matters once an install accumulates a few hundred
// backups.
type CatalogEntry struct {
	ID            string
	FilePath      string
	Scope         string
	ScopeLevel    string
	Slug          string
	WorkspaceID   string
	CreatedAt     time.Time
	CreatedBy     string
	Size          int64
	SHA256        string
	Encrypted     bool
	FormatVersion int

	// Kind is full | custom | environments | instance. Empty on write
	// means full.
	Kind string
	// PlanID / RunID name the backup plan and run that produced the
	// bundle; empty for bundles made by hand or before runs existed.
	PlanID string
	RunID  string
	// Pinned bundles are never deleted by any retention rule.
	Pinned bool
	// ProofLevel: 1 checksum, 2 contents checked, 3 test restore. Zero on
	// write means 1 — a bundle that was just written has its checksum.
	ProofLevel     int
	ProofCheckedAt *time.Time
	// DrillResult / DrillAt / DrillReport: the last test restore, if any.
	// DrillResult is ok | partial | failed.
	DrillResult string
	DrillAt     *time.Time
	DrillReport json.RawMessage
	// Incomplete is the manifest's contents.incomplete. nil means "not
	// recorded" (a row written before the column existed) — never read it
	// as complete; an empty, non-nil slice means the bundle recorded no gap.
	Incomplete []IncompleteItem
}

// catalogExecer is what the per-path mutators need — a *sql.DB or the
// *sql.Tx an API handler also writes its audit entry in.
type catalogExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// UpsertCatalogEntry inserts a row for the bundle at path. If the row
// already exists (admin created the same bundle twice via a retry) we
// overwrite the mutable fields — size, checksum, timestamp — so the
// catalog never drifts from reality.
func UpsertCatalogEntry(ctx context.Context, db *sql.DB, e CatalogEntry) error {
	if db == nil {
		return nil
	}
	return upsertCatalogEntry(ctx, db, e)
}

func upsertCatalogEntry(ctx context.Context, db catalogExecer, e CatalogEntry) error {
	if e.ID == "" {
		id, err := newCatalogID()
		if err != nil {
			return err
		}
		e.ID = id
	}
	scopeLevel := e.ScopeLevel
	if scopeLevel == "" {
		scopeLevel = string(DefaultScopeLevel)
	}
	kind := e.Kind
	if kind == "" {
		kind = KindFull
	}
	proof := e.ProofLevel
	if proof <= 0 {
		proof = ProofChecksum
	}
	var incomplete any
	if e.Incomplete != nil {
		b, err := json.Marshal(e.Incomplete)
		if err != nil {
			return fmt.Errorf("backup: encode catalog incomplete: %w", err)
		}
		incomplete = string(b)
	}
	// On conflict (the same path catalogued again — a retry, or the startup
	// scan) the bundle's own facts are refreshed, but what an operator or a
	// check established about it — the pin, the drill — is kept. The proof
	// level is kept too unless the payload checksum changed, in which case
	// it is a different bundle under the same name and starts over.
	_, err := db.ExecContext(ctx, `
INSERT INTO backup_catalog
  (id, file_path, scope, scope_level, slug, workspace_id, created_at, created_by,
   size, sha256, encrypted, format_version, kind, plan_id, run_id, pinned,
   proof_level, proof_checked_at, incomplete)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_path) DO UPDATE SET
  scope            = excluded.scope,
  scope_level      = excluded.scope_level,
  slug             = excluded.slug,
  workspace_id     = excluded.workspace_id,
  created_at       = excluded.created_at,
  created_by       = excluded.created_by,
  size             = excluded.size,
  proof_level      = CASE WHEN backup_catalog.sha256 = excluded.sha256 THEN backup_catalog.proof_level ELSE excluded.proof_level END,
  proof_checked_at = CASE WHEN backup_catalog.sha256 = excluded.sha256 THEN backup_catalog.proof_checked_at ELSE excluded.proof_checked_at END,
  sha256           = excluded.sha256,
  encrypted        = excluded.encrypted,
  format_version   = excluded.format_version,
  kind             = excluded.kind,
  plan_id          = COALESCE(excluded.plan_id, backup_catalog.plan_id),
  run_id           = COALESCE(excluded.run_id, backup_catalog.run_id),
  incomplete       = COALESCE(excluded.incomplete, backup_catalog.incomplete)
`,
		e.ID, e.FilePath, e.Scope, scopeLevel, e.Slug, e.WorkspaceID,
		e.CreatedAt.UTC().Format(time.RFC3339), e.CreatedBy,
		e.Size, e.SHA256, boolToInt(e.Encrypted), e.FormatVersion,
		kind, nullableString(e.PlanID), nullableString(e.RunID), boolToInt(e.Pinned),
		proof, nullableTime(e.ProofCheckedAt), incomplete,
	)
	if err != nil {
		return fmt.Errorf("backup: upsert catalog: %w", err)
	}
	return nil
}

// DeleteCatalogEntry removes the row keyed by path. Safe to call for
// a bundle that was never catalogued (legacy pre-v49 ones) — the
// DELETE is a no-op then.
func DeleteCatalogEntry(ctx context.Context, db *sql.DB, path string) error {
	if db == nil {
		return nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM backup_catalog WHERE file_path = ?`, path); err != nil {
		return fmt.Errorf("backup: delete catalog: %w", err)
	}
	return nil
}

// ReconcileCatalog removes catalog rows whose backing file no longer
// exists on disk. Returns the paths it pruned. Used by the List
// handler and startup to keep the admin UI honest when bundles are
// deleted out of band — the historical drift sources are
// pre-CRE-128 rotates that removed files without syncing the catalog
// and the test rig's `rm` of bundle files. workspaceID scopes the
// reconcile so an admin pruning their own list cannot accidentally
// touch another workspace's rows.
//
// Only os.ErrNotExist triggers a prune. Any other Stat error
// (permission denied, transient I/O failure on a remote backend) is
// left alone — vacuuming the entire catalog on a flaky NFS would
// hurt more than the drift.
func ReconcileCatalog(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	cat, err := ListCatalog(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	st := getDefaultStorage()
	var pruned []string
	for _, e := range cat {
		if _, err := st.Stat(ctx, e.FilePath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if delErr := DeleteCatalogEntry(ctx, db, e.FilePath); delErr != nil {
			// Surface the partial progress alongside the error so the
			// caller (admin List handler) can still log "we pruned
			// these N before hitting a DB issue" without losing the
			// failure signal.
			return pruned, fmt.Errorf("backup: reconcile delete %s: %w", e.FilePath, delErr)
		}
		pruned = append(pruned, e.FilePath)
	}
	return pruned, nil
}

// ListCatalog returns the catalogued bundles sorted by created_at
// descending (newest first, matching the CLI). Optional workspaceID
// filter scopes the result to one workspace when non-empty.
func ListCatalog(ctx context.Context, db *sql.DB, workspaceID string) ([]CatalogEntry, error) {
	if db == nil {
		return nil, nil
	}
	const baseQuery = `SELECT ` + catalogColumns + ` FROM backup_catalog`
	// Two distinct queries keep the driver's parameter plane typed —
	// []any with an optional first element is easy to get wrong when
	// the WHERE clause grows.
	var rows *sql.Rows
	var err error
	if workspaceID != "" {
		rows, err = db.QueryContext(ctx,
			baseQuery+` WHERE workspace_id = ? ORDER BY created_at DESC`,
			workspaceID)
	} else {
		rows, err = db.QueryContext(ctx,
			baseQuery+` ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("backup: list catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CatalogEntry
	for rows.Next() {
		e, err := scanCatalogEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate catalog: %w", err)
	}
	return out, nil
}

// catalogColumns is the SELECT list scanCatalogEntry reads, in order.
const catalogColumns = `id, file_path, scope, COALESCE(scope_level, 'standard'), COALESCE(slug, ''),
       COALESCE(workspace_id, ''),
       created_at, COALESCE(created_by, ''), size, sha256, encrypted, format_version,
       COALESCE(kind, 'full'), COALESCE(plan_id, ''), COALESCE(run_id, ''), COALESCE(pinned, 0),
       COALESCE(proof_level, 1), proof_checked_at, COALESCE(drill_result, ''), drill_at,
       drill_report, incomplete`

type rowScanner interface {
	Scan(dest ...any) error
}

func parseCatalogTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func scanCatalogEntry(rows rowScanner) (CatalogEntry, error) {
	var e CatalogEntry
	var enc, pinned int
	var createdAt string
	var proofAt, drillAt, drillReport, incomplete sql.NullString
	if err := rows.Scan(&e.ID, &e.FilePath, &e.Scope, &e.ScopeLevel, &e.Slug, &e.WorkspaceID,
		&createdAt, &e.CreatedBy, &e.Size, &e.SHA256, &enc, &e.FormatVersion,
		&e.Kind, &e.PlanID, &e.RunID, &pinned, &e.ProofLevel, &proofAt, &e.DrillResult, &drillAt,
		&drillReport, &incomplete); err != nil {
		return e, fmt.Errorf("backup: scan catalog row: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return e, fmt.Errorf("backup: parse created_at %q for %s: %w", createdAt, e.ID, err)
	}
	e.CreatedAt = parsed
	e.Encrypted = enc == 1
	e.Pinned = pinned == 1
	if e.ProofCheckedAt, err = parseCatalogTime(proofAt); err != nil {
		return e, fmt.Errorf("backup: parse proof_checked_at for %s: %w", e.ID, err)
	}
	if e.DrillAt, err = parseCatalogTime(drillAt); err != nil {
		return e, fmt.Errorf("backup: parse drill_at for %s: %w", e.ID, err)
	}
	if drillReport.Valid && drillReport.String != "" {
		e.DrillReport = json.RawMessage(drillReport.String)
	}
	if incomplete.Valid && incomplete.String != "" {
		if err := json.Unmarshal([]byte(incomplete.String), &e.Incomplete); err != nil {
			return e, fmt.Errorf("backup: decode incomplete for %s: %w", e.ID, err)
		}
		if e.Incomplete == nil {
			e.Incomplete = []IncompleteItem{}
		}
	}
	return e, nil
}

// GetCatalogEntry returns the row for path, or ErrCatalogEntryNotFound.
func GetCatalogEntry(ctx context.Context, db *sql.DB, path string) (CatalogEntry, error) {
	e, err := scanCatalogEntry(db.QueryRowContext(ctx, `SELECT `+catalogColumns+` FROM backup_catalog WHERE file_path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrCatalogEntryNotFound
	}
	return e, err
}

func requireOneRow(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("backup: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("backup: %s: %w", what, err)
	}
	if n == 0 {
		return ErrCatalogEntryNotFound
	}
	return nil
}

// PinCatalogEntry marks the bundle at path as pinned: no retention rule
// deletes it until it is unpinned. ErrCatalogEntryNotFound when the bundle
// is not catalogued.
func PinCatalogEntry(ctx context.Context, ex catalogExecer, path string) error {
	res, err := ex.ExecContext(ctx, `UPDATE backup_catalog SET pinned = 1 WHERE file_path = ?`, path)
	return requireOneRow(res, err, "pin catalog entry")
}

// UnpinCatalogEntry clears the pin; retention rules apply again.
func UnpinCatalogEntry(ctx context.Context, ex catalogExecer, path string) error {
	res, err := ex.ExecContext(ctx, `UPDATE backup_catalog SET pinned = 0 WHERE file_path = ?`, path)
	return requireOneRow(res, err, "unpin catalog entry")
}

// SetCatalogProof records that the bundle at path was shown to be good to
// level (ProofChecksum, ProofContents or ProofRestore) at at. It sets the
// level as given — a failed check may lower it.
func SetCatalogProof(ctx context.Context, ex catalogExecer, path string, level int, at time.Time) error {
	if level < ProofChecksum || level > ProofRestore {
		return fmt.Errorf("backup: proof level %d is not 1, 2 or 3", level)
	}
	res, err := ex.ExecContext(ctx, `UPDATE backup_catalog SET proof_level = ?, proof_checked_at = ? WHERE file_path = ?`,
		level, at.UTC().Format(time.RFC3339), path)
	return requireOneRow(res, err, "set catalog proof")
}

// SetCatalogDrill records a test restore of the bundle at path. result is
// ok | partial | failed; report is the drill's JSON report. An ok or
// partial drill raises the proof level to ProofRestore (a partial one is
// still shown as partial through drill_result); a failed drill proves
// nothing and leaves the level alone.
func SetCatalogDrill(ctx context.Context, ex catalogExecer, path, result string, report json.RawMessage, at time.Time) error {
	switch result {
	case RestoreResultOK, RestoreResultPartial, RestoreResultFailed:
	default:
		return fmt.Errorf("backup: drill result %q is not ok, partial or failed", result)
	}
	if len(report) == 0 {
		report = json.RawMessage("{}")
	}
	if !json.Valid(report) {
		return fmt.Errorf("backup: drill report is not valid JSON")
	}
	stamp := at.UTC().Format(time.RFC3339)
	res, err := ex.ExecContext(ctx, `
UPDATE backup_catalog SET
  drill_result = ?, drill_at = ?, drill_report = ?,
  proof_level      = CASE WHEN ? IN ('ok', 'partial') AND proof_level < 3 THEN 3 ELSE proof_level END,
  proof_checked_at = CASE WHEN ? IN ('ok', 'partial') THEN ? ELSE proof_checked_at END
WHERE file_path = ?`, result, stamp, string(report), result, result, stamp, path)
	return requireOneRow(res, err, "set catalog drill")
}

// BackfillCatalogFromDir walks the default bundles directory and
// upserts a catalog row for every bundle that ListBackups recognises.
// Used once at startup so installs upgraded to v49 see their existing
// bundles in the admin UI without a manual `backup list` run. The
// walk is idempotent via the UNIQUE(file_path) constraint, so running
// it on every boot stays cheap.
//
// Errors reading individual bundles are logged via the supplied
// logger (if any) but do not abort — a single corrupted file should
// not block startup.
func BackfillCatalogFromDir(ctx context.Context, db *sql.DB, dir string, logger func(string)) error {
	if db == nil || dir == "" {
		return nil
	}
	entries, err := ListBackups(ctx, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, le := range entries {
		manifest, err := Inspect(ctx, le.Path)
		if err != nil {
			if logger != nil {
				logger(fmt.Sprintf("backup catalog backfill: skip %s: %v", le.Path, err))
			}
			continue
		}
		level := string(manifest.ScopeLevel)
		if level == "" {
			level = string(DefaultScopeLevel)
		}
		entry := CatalogEntry{
			FilePath:      le.Path,
			Scope:         string(manifest.Scope),
			ScopeLevel:    level,
			Size:          le.Size,
			SHA256:        manifest.Checksums.PayloadSHA256,
			Encrypted:     manifest.Encryption.Enabled,
			FormatVersion: manifest.FormatVersion,
			CreatedAt:     manifest.CreatedAt,
			CreatedBy:     manifest.CreatedBy.Email,
			Incomplete:    manifestIncomplete(manifest),
		}
		if manifest.Contents.Workspace != nil {
			entry.WorkspaceID = manifest.Contents.Workspace.ID
			entry.Slug = manifest.Contents.Workspace.Slug
		}
		if manifest.Scope == ScopeCrew && len(manifest.Contents.Crews) > 0 {
			entry.Slug = manifest.Contents.Crews[0].Slug
		}
		if err := UpsertCatalogEntry(ctx, db, entry); err != nil && logger != nil {
			logger(fmt.Sprintf("backup catalog backfill: upsert %s: %v", le.Path, err))
		}
	}
	return nil
}

// newCatalogID returns a random 128-bit hex string. We intentionally
// do not reuse the CUID generator from internal/api — it lives behind
// a dependency boundary we are not willing to invert.
//
// Propagates rand.Read errors rather than swallowing them: if the OS
// entropy source is unavailable we MUST abort the catalogue write —
// continuing with a fixed/empty ID risks duplicate rows and later
// constraint failures on backup_catalog.id PRIMARY KEY.
func newCatalogID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("backup: generate catalog id: %w", err)
	}
	return "bk_" + hex.EncodeToString(b[:]), nil
}

// manifestIncomplete returns the manifest's recorded gaps as a non-nil
// slice, so the catalog stores "recorded: none" ([]) rather than "not
// recorded" (NULL) for every bundle whose manifest we have read. Bundles
// written before Incomplete existed still get [] here, but their older
// per-kind fields are folded in, so a pre-existing bundle that recorded
// missing containers or memory blobs is not catalogued as complete.
func manifestIncomplete(m *Manifest) []IncompleteItem {
	out := []IncompleteItem{}
	if m == nil {
		return out
	}
	if len(m.Contents.Incomplete) > 0 {
		return append(out, m.Contents.Incomplete...)
	}
	wsID := ""
	if m.Contents.Workspace != nil {
		wsID = m.Contents.Workspace.ID
	}
	if m.Contents.MemoryBlobsMissing > 0 {
		out = append(out, IncompleteItem{Kind: IncompleteMemoryBlobMissing, Count: m.Contents.MemoryBlobsMissing, Workspace: wsID,
			Detail: fmt.Sprintf("%d memory version(s) had no content file in the version store", m.Contents.MemoryBlobsMissing)})
	}
	for _, slug := range m.Contents.MissingContainerCrews {
		out = append(out, IncompleteItem{Kind: IncompleteContainerMissing, Count: 1, Workspace: wsID,
			Detail: fmt.Sprintf("crew %s had a provisioned container that was gone when the backup ran", slug)})
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CatalogEntryFromResult builds a CatalogEntry from a completed
// CreateResult plus the manifest that was embedded in the bundle.
// Separating it from CreateBackup keeps the runner free of DB-layout
// concerns — the caller (CLI / REST handler) calls UpsertCatalogEntry
// once a successful CreateResult comes back.
func CatalogEntryFromResult(res *CreateResult, m *Manifest) CatalogEntry {
	level := string(m.ScopeLevel)
	if level == "" {
		level = string(DefaultScopeLevel)
	}
	e := CatalogEntry{
		FilePath:      res.Path,
		Scope:         string(m.Scope),
		ScopeLevel:    level,
		Size:          res.Size,
		SHA256:        res.SHA256,
		Encrypted:     m.Encryption.Enabled,
		FormatVersion: m.FormatVersion,
		CreatedAt:     m.CreatedAt,
		CreatedBy:     m.CreatedBy.Email,
		Kind:          KindFull,
		ProofLevel:    ProofChecksum,
		Incomplete:    manifestIncomplete(m),
	}
	if m.Kind == KindCustom {
		e.Kind = KindCustom
	}
	if m.Contents.Workspace != nil {
		e.WorkspaceID = m.Contents.Workspace.ID
		e.Slug = m.Contents.Workspace.Slug
	}
	if m.Scope == ScopeCrew && len(m.Contents.Crews) > 0 {
		e.Slug = m.Contents.Crews[0].Slug
	}
	// Bundle path base-name fallback when a crew scope bundle has no
	// crews summary for some reason — extremely unlikely but keeps the
	// catalog row non-empty in the slug slot.
	if e.Slug == "" {
		base := filepath.Base(res.Path)
		e.Slug = strings.TrimSuffix(base, ".tar.zst")
	}
	return e
}
