package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// debugReadBuildInfo is a tiny indirection so tests can substitute a
// deterministic version string. The real path consults runtime/debug.
func debugReadBuildInfo() (string, bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	return bi.Main.Version, true
}

// Scope-qualified filename helpers.

// DefaultBackupsDir is the on-disk location the runner defaults to
// when --output is not specified. Callers can override it via
// CreateOptions.OutputDir. Uses the package-level default StorageOps
// (LocalStorageOps unless swapped by tests via SetDefaultStorage).
// CreateBackup uses defaultBackupsDirFor so the per-call storage
// override (CreateOptions.Storage) derives its default path from the
// same backend that will write the bundle.
func DefaultBackupsDir() (string, error) {
	return defaultBackupsDirFor(getDefaultStorage())
}

func defaultBackupsDirFor(st StorageOps) (string, error) {
	home, err := st.Home()
	if err != nil {
		return "", fmt.Errorf("backup: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".crewship", "backups"), nil
}

// BundleFileName returns the canonical filename for a new bundle:
//
//	crewship-<scope>-<slug>-<ISO-timestamp>.tar.zst
func BundleFileName(scope Scope, slug string, ts time.Time) string {
	stamp := ts.UTC().Format("20060102T150405Z")
	return fmt.Sprintf("crewship-%s-%s-%s.tar.zst", scope, slug, stamp)
}

// CreateOptions / CreateResult / CreateBackup live in runner_create.go.
// RestoreOptions / RestoreResult / RestoreBackup live in runner_restore.go.

// ListBackups returns metadata for every bundle currently in dir. The
// result is stable-ordered by CreatedAt descending so the newest
// bundle is first (matches what most users want in the CLI output).
func ListBackups(ctx context.Context, dir string) ([]ListEntry, error) {
	// Capture the backend once so every entry in this list is
	// inspected against the SAME storage. Without the capture a
	// concurrent SetDefaultStorage swap could have ReadDir return
	// paths from one backend and Inspect read manifests from another,
	// silently returning stale or unrelated data.
	st := getDefaultStorage()
	entries, err := st.ReadDir(ctx, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: list %s: %w", dir, err)
	}
	var out []ListEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".zst" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		m, inspectErr := inspectWithStorage(ctx, st, path)
		le := ListEntry{
			Path: path,
			Size: info.Size(),
			// Fall back to the filesystem mtime so sort has a
			// reasonable ordering even when Inspect cannot read the
			// manifest (corrupted bundle, permission issue).
			CreatedAt: info.ModTime(),
		}
		if inspectErr == nil && m != nil {
			le.Scope = m.Scope
			le.ScopeLevel = m.ScopeLevel
			if le.ScopeLevel == "" {
				le.ScopeLevel = DefaultScopeLevel
			}
			le.Encrypted = m.Encryption.Enabled
			le.CreatedAt = m.CreatedAt
			le.FormatVersion = m.FormatVersion
			if m.Contents.Workspace != nil {
				le.WorkspaceID = m.Contents.Workspace.ID
			}
		}
		out = append(out, le)
	}
	// Newest-first ordering is part of the ListBackups contract
	// (StorageOps.ReadDir leaves ordering undefined), so a remote
	// backend would otherwise return entries in arbitrary order.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// ListEntry is the row format emitted by ListBackups and surfaced by
// the CLI `crewship backup list` output.
//
// WorkspaceID is populated from the manifest when the bundle is a
// workspace-scope backup, so callers (Rotate, the /admin/backups
// handler's per-workspace filter) can cheaply route bundles without
// re-parsing the manifest a second time.
type ListEntry struct {
	Path          string
	Size          int64
	Scope         Scope
	ScopeLevel    ScopeLevel
	Encrypted     bool
	CreatedAt     time.Time
	FormatVersion int
	WorkspaceID   string
}

// Inspect opens a bundle and returns its manifest without decrypting
// the payload. Used by `crewship backup inspect` and ListBackups.
func Inspect(ctx context.Context, path string) (*Manifest, error) {
	return inspectWithStorage(ctx, getDefaultStorage(), path)
}

// inspectWithStorage is the shared body behind Inspect. Having a
// storage-parameterised variant lets ListBackups / Delete keep a
// single StorageOps for the whole operation — otherwise a concurrent
// SetDefaultStorage swap could make iteration and per-entry inspect
// talk to different backends.
func inspectWithStorage(ctx context.Context, st StorageOps, path string) (*Manifest, error) {
	f, err := st.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	m, _, err := ReadBundle(f)
	if err != nil {
		return m, err
	}
	return m, nil
}

// VerifyResult is returned by Verify and reports whether the bundle
// at the given path is structurally valid and passes its recorded
// SHA-256 checksum. Mismatch or decryption failure produces a non-nil
// Err with the specific reason; Valid summarises.
type VerifyResult struct {
	Manifest *Manifest
	Valid    bool
	Size     int64
	Err      error
}

// Verify opens a bundle, reads the manifest, and streams the sealed
// payload through HashingReader to confirm the SHA-256 recorded in
// the manifest still matches. Unlike Inspect it does NOT stop at
// the manifest — it walks the whole payload — but it never decrypts
// (the checksum covers the sealed bytes, so no key is needed).
//
// Used by `crewship backup verify <file>` so admins can confirm a
// stored bundle is still restorable without actually committing to a
// restore against a test instance.
func Verify(ctx context.Context, path string) (*VerifyResult, error) {
	st := getDefaultStorage()
	info, err := st.Stat(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("backup: stat %s: %w", path, err)
	}
	f, err := st.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	manifest, sealed, closeBundle, err := ReadBundleStream(f)
	if err != nil {
		return &VerifyResult{Manifest: manifest, Valid: false, Size: info.Size(), Err: err}, nil
	}
	defer func() {
		if closeBundle != nil {
			_ = closeBundle()
		}
	}()

	hashed := NewHashingReader(sealed)
	if _, err := io.Copy(io.Discard, hashed); err != nil {
		return &VerifyResult{Manifest: manifest, Valid: false, Size: info.Size(), Err: err}, nil
	}
	if err := VerifyChecksum(manifest.Checksums.PayloadSHA256, hashed.Sum()); err != nil {
		return &VerifyResult{Manifest: manifest, Valid: false, Size: info.Size(), Err: err}, nil
	}
	return &VerifyResult{Manifest: manifest, Valid: true, Size: info.Size()}, nil
}

// ForceReleaseLock deletes the backup_lock row for the given
// workspace regardless of owner or TTL. Emergency escape hatch for
// a crashed backup whose defer release did not fire. Callers must
// gate this behind an explicit operator action (CLI `backup unlock
// --force`, admin UI confirmation) and emit an audit log row — the
// function itself enforces no policy.
func ForceReleaseLock(ctx context.Context, db *sql.DB, workspaceID string) error {
	if db == nil || workspaceID == "" {
		return fmt.Errorf("backup: ForceReleaseLock: db and workspaceID required")
	}
	_, err := db.ExecContext(ctx, `DELETE FROM backup_locks WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return fmt.Errorf("backup: force release lock: %w", err)
	}
	return nil
}

// Rotate enumerates bundles in dir (via ListBackups), filters them
// by workspace (bundle.Manifest.Contents.Workspace.ID == workspaceID
// — so an admin of workspace A does not accidentally rotate workspace
// B's backups), sorts by CreatedAt descending, and deletes anything
// beyond keepLast or older than cutoff. dryRun returns the list of
// paths that WOULD be deleted without touching disk.
//
// keepLast ≤ 0 disables the count-based rule; keepDays ≤ 0 disables
// the age-based rule. When both are set, both are applied (a bundle
// survives only if it is within keepLast AND newer than cutoff).
func Rotate(ctx context.Context, dir, workspaceID string, keepLast int, keepDays int, dryRun bool) ([]string, error) {
	entries, err := ListBackups(ctx, dir)
	if err != nil {
		return nil, err
	}
	// Filter to the caller's workspace. ListBackups already parsed
	// each bundle's manifest to populate WorkspaceID + CreatedAt, so
	// we avoid a second Inspect per bundle here — meaningful on a
	// directory with hundreds of backups.
	var scoped []ListEntry
	for _, e := range entries {
		if e.WorkspaceID == "" || e.WorkspaceID != workspaceID {
			continue
		}
		scoped = append(scoped, e)
	}
	// Newest first so keepLast drops the tail.
	for i := 1; i < len(scoped); i++ {
		for j := i; j > 0 && scoped[j-1].CreatedAt.Before(scoped[j].CreatedAt); j-- {
			scoped[j], scoped[j-1] = scoped[j-1], scoped[j]
		}
	}
	now := time.Now().UTC()
	var cutoff time.Time
	if keepDays > 0 {
		cutoff = now.AddDate(0, 0, -keepDays)
	}

	var toDelete []string
	for i, e := range scoped {
		drop := false
		if keepLast > 0 && i >= keepLast {
			drop = true
		}
		if keepDays > 0 && e.CreatedAt.Before(cutoff) {
			drop = true
		}
		if drop {
			toDelete = append(toDelete, e.Path)
		}
	}
	if dryRun {
		return toDelete, nil
	}
	for _, p := range toDelete {
		if err := Delete(ctx, p); err != nil {
			return toDelete, err
		}
	}
	return toDelete, nil
}

// Delete removes a bundle. Before touching disk it verifies the file
// is actually a Crewship backup — the name ends with .tar.zst AND the
// outer tar yields a valid MANIFEST.json. This guard prevents a
// mis-click from using the delete endpoint as a generic rm primitive
// for anything that passed API-level path validation.
//
// Callers emit an audit log row after a successful delete.
func Delete(ctx context.Context, path string) error {
	if !strings.HasSuffix(path, ".tar.zst") {
		return fmt.Errorf("backup: refuse to delete %q: not a .tar.zst bundle", path)
	}
	st := getDefaultStorage()
	if _, err := inspectWithStorage(ctx, st, path); err != nil {
		return fmt.Errorf("backup: refuse to delete %q: failed inspect (%v)", path, err)
	}
	if err := st.Remove(ctx, path); err != nil {
		return fmt.Errorf("backup: delete %s: %w", path, err)
	}
	return nil
}

// ensureAgentsIdle refuses the backup if any agent in the scope has
// status 'running' or 'busy'. The check runs in a single query against
// the agents table; absent column / absent table = treat as OK so the
// guard never blocks a freshly-seeded install.
func ensureAgentsIdle(ctx context.Context, db *sql.DB, target *WorkspaceTarget) error {
	var exists int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agents'`,
	).Scan(&exists)
	if err != nil || exists == 0 {
		return nil
	}
	if hasStatus, _ := columnExists(ctx, db, "agents", "status"); !hasStatus {
		return nil
	}
	crewIDs := make([]string, 0, len(target.CrewTargets))
	for _, c := range target.CrewTargets {
		crewIDs = append(crewIDs, c.ID)
	}
	if len(crewIDs) == 0 {
		return nil
	}
	// Build a placeholder set of ? for the crew IDs. Variadic to []any
	// conversion happens only at the DB boundary so the rest of the
	// function keeps its string typing.
	placeholders := "?"
	for i := 1; i < len(crewIDs); i++ {
		placeholders += ",?"
	}
	query := fmt.Sprintf(
		`SELECT COALESCE(slug, id) FROM agents WHERE crew_id IN (%s) AND status IN ('running','busy') LIMIT 1`,
		placeholders,
	)
	args := make([]any, len(crewIDs))
	for i, id := range crewIDs {
		args[i] = id
	}
	var running string
	err = db.QueryRowContext(ctx, query, args...).Scan(&running)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: agent-idle guard query: %w", err)
	}
	return fmt.Errorf("%w: agent %q; abort the run or wait for it to finish", ErrAgentRunning, running)
}

// columnExists reports whether table.column is present in the current
// DB. Used only by the agent-idle guard to cope with older schemas.
func columnExists(ctx context.Context, db *sql.DB, table, col string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// cleanupStalePartials removes *.partial files older than maxAge in
// dir, OPTIONALLY narrowed to a single workspace/crew slug. A crashed
// or cancelled CreateBackup leaves one behind; without this sweep they
// accumulate forever. Errors are swallowed — the only consequence of a
// failed cleanup is a file that will be retried on the next backup,
// not a correctness issue.
//
// Bundle filenames follow `crewship-<scope>-<slug>-<ts>.tar.zst[.partial]`
// (see BundleFileName). When ownerSlug is non-empty, only `.partial`
// files whose name contains the corresponding `-<slug>-` segment are
// swept; otherwise every stale `.partial` qualifies (the old
// behaviour, kept for callers that don't know which slug they're
// cleaning up for — e.g. an admin-side sweeper).
//
// The scoped form prevents the classic multi-tenant footgun: workspace
// A starts a large backup whose `.partial` is alive for >1h; meanwhile
// workspace B fires its own CreateBackup, the unconditional sweep
// removes A's still-active `.partial`, and A's restore mid-write hits
// a "file removed under us" error after acquiring the lock.
func cleanupStalePartials(ctx context.Context, st StorageOps, dir, ownerSlug string, maxAge time.Duration) {
	if st == nil {
		st = getDefaultStorage()
	}
	entries, err := st.ReadDir(ctx, dir)
	if err != nil {
		return
	}
	// `-<slug>-` rather than `<slug>` so a slug that is a substring of
	// another workspace's slug doesn't accidentally match. The dashes
	// are part of BundleFileName's contract (`crewship-<scope>-<slug>-`).
	var slugInfix string
	if ownerSlug != "" {
		slugInfix = "-" + ownerSlug + "-"
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".partial") {
			continue
		}
		if slugInfix != "" && !strings.Contains(name, slugInfix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = st.Remove(ctx, filepath.Join(dir, e.Name()))
		}
	}
}

// currentInstance collects hostname / platform details for the manifest.
func currentInstance() Instance {
	host, _ := os.Hostname()
	return Instance{
		Hostname: host,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// AppliedMigrationVersions returns the list of migration versions
// recorded in the _migrations table, sorted ascending. A missing
// table or a read error returns an empty slice so the caller can
// always populate manifest.SchemaMigrationVersions without nil
// handling — the manifest treats [] as "unknown", not broken.
func AppliedMigrationVersions(ctx context.Context, db *sql.DB) []int {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version FROM _migrations ORDER BY version`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return out
		}
		out = append(out, v)
	}
	return out
}

// DetectCrewshipVersion returns a best-effort version string for the
// running binary. The build system typically injects it via
// -ldflags "-X main.Version=…"; we fall back to the module's
// ReadBuildInfo version (often "(devel)" in local dev) so the
// manifest always records something non-empty.
func DetectCrewshipVersion(override string) string {
	if override != "" {
		return override
	}
	if env := os.Getenv("CREWSHIP_VERSION"); env != "" {
		return env
	}
	if bi, ok := debugReadBuildInfo(); ok && bi != "" {
		return bi
	}
	return "dev"
}
