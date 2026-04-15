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
	"strings"
	"time"

	"filippo.io/age"
)

// Scope-qualified filename helpers.

// DefaultBackupsDir is the on-disk location the runner defaults to
// when --output is not specified. Callers can override it via
// CreateOptions.OutputDir.
func DefaultBackupsDir() (string, error) {
	home, err := os.UserHomeDir()
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

// CreateOptions collects the parameters for CreateBackup. The runner
// takes these rather than a long positional list so call sites (CLI,
// REST handler) read cleanly.
type CreateOptions struct {
	Scope           Scope  // ScopeCrew or ScopeWorkspace (instance is PR 4)
	WorkspaceID     string // required for Scope=workspace
	CrewID          string // required for Scope=crew
	OutputDir       string // defaults to ~/.crewship/backups
	CrewshipVersion string // for manifest.CrewshipVersionAtBackup
	// Actor is stamped into the manifest and audit log.
	Actor Actor
	// Encryption — exactly one of Passphrase, Recipients, or NoEncrypt
	// must be set. The CLI / API layers enforce this before we get here,
	// but WriteBundle will reject the bad input regardless.
	Passphrase string
	Recipients []age.Recipient
	NoEncrypt  bool
	// SchemaMigrationVersions is the list of DB migrations applied on
	// the source instance at backup time. Typically produced by the
	// migration subsystem; caller passes it through unchanged.
	SchemaMigrationVersions []int
	// CrewContainerName maps a crew slug to a Docker container name
	// (the provider owns this). Nil is valid for tests.
	CrewContainerName func(slug string) string
	// DockerOps executes pause/unpause/CopyFrom against the daemon.
	DockerOps DockerOps
}

// Validate returns an error if opts lack the fields required by its
// scope. Called by CreateBackup before any side effects.
func (o *CreateOptions) Validate() error {
	switch o.Scope {
	case ScopeWorkspace:
		if o.WorkspaceID == "" {
			return fmt.Errorf("backup: CreateOptions.WorkspaceID required for workspace scope")
		}
	case ScopeCrew:
		if o.CrewID == "" {
			return fmt.Errorf("backup: CreateOptions.CrewID required for crew scope")
		}
	default:
		return fmt.Errorf("backup: unsupported scope %q", o.Scope)
	}
	if o.Actor.UserID == "" {
		return fmt.Errorf("backup: CreateOptions.Actor.UserID required")
	}
	if err := RequireAdmin(o.Actor.Role); err != nil {
		return err
	}
	if o.Passphrase == "" && len(o.Recipients) == 0 && !o.NoEncrypt {
		return fmt.Errorf("backup: must supply Passphrase, Recipients, or NoEncrypt=true")
	}
	return nil
}

// CreateResult is returned by CreateBackup on success.
type CreateResult struct {
	Path     string
	Size     int64
	SHA256   string
	Manifest *Manifest
}

// LockTimeout is how long CreateBackup will hold the advisory lock
// before its TTL kicks in and allows reclamation. Matches the
// DefaultLockTTL used by the lock manager.
const LockTimeout = DefaultLockTTL

// CreateBackup runs the full workspace / crew backup flow:
//
//  1. Validate options and RBAC.
//  2. Resolve targets (workspace + crews with container IDs).
//  3. Acquire the per-workspace advisory lock.
//  4. Check agent-idle guard (no agents currently running).
//  5. Pause each crew, stream its data into the payload tar, unpause.
//  6. Dump DB rows.
//  7. Seal the payload (AGE) and wrap it in the outer bundle.
//  8. Atomic rename .partial → final path.
//  9. Audit log row.
//
// All steps release resources on error; if unpause fails after a
// successful tar, the error surfaces as ErrPauseUnpauseLost so the
// caller can alert an operator.
func CreateBackup(ctx context.Context, db *sql.DB, opts CreateOptions) (*CreateResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	// 1. Resolve targets.
	var target *WorkspaceTarget
	var err error
	switch opts.Scope {
	case ScopeWorkspace:
		target, err = LoadWorkspaceTarget(ctx, db, opts.WorkspaceID, opts.CrewContainerName)
	case ScopeCrew:
		target, err = LoadCrewTarget(ctx, db, opts.CrewID, opts.CrewContainerName)
	}
	if err != nil {
		return nil, err
	}

	// 2. Acquire lock (per-workspace).
	lockMgr := NewSQLLockManager(db)
	release, err := lockMgr.AcquireWorkspaceLock(ctx, target.ID, opts.Actor.UserID, LockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release(context.Background()) }()

	// 3. Agent idle guard — refuse if any agent is actively running.
	if err := ensureAgentsIdle(ctx, db, target); err != nil {
		return nil, err
	}

	// 4. Output directory.
	outDir := opts.OutputDir
	if outDir == "" {
		outDir, err = DefaultBackupsDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: ensure output dir: %w", err)
	}

	// 5. Build the payload tar to a temp file so peak memory is bounded
	// by the zstd encoder's window (a few MB) rather than the full
	// workspace size. A multi-GB workspace therefore stays within
	// reasonable RAM even on modest hosts.
	now := time.Now().UTC()
	payloadFile, err := os.CreateTemp("", "crewship-backup-payload-*.tar.zst")
	if err != nil {
		return nil, fmt.Errorf("backup: create payload temp: %w", err)
	}
	payloadPath := payloadFile.Name()
	defer func() { _ = os.Remove(payloadPath) }()

	payloadWriter, err := NewTarZstWriter(payloadFile)
	if err != nil {
		_ = payloadFile.Close()
		return nil, err
	}

	// 5a. Per-crew live data.
	for _, crew := range target.CrewTargets {
		if opts.DockerOps != nil && crew.ContainerID != "" {
			if err := CollectCrew(ctx, opts.DockerOps, payloadWriter, crew); err != nil {
				_ = payloadWriter.Close()
				_ = payloadFile.Close()
				return nil, err
			}
		}
	}

	// 5b. Devcontainer / mise config per crew.
	if err := WriteDevcontainerSection(payloadWriter, target.CrewTargets, now); err != nil {
		_ = payloadWriter.Close()
		_ = payloadFile.Close()
		return nil, err
	}

	// 5c. DB dump.
	var dump *DBDump
	switch opts.Scope {
	case ScopeWorkspace:
		dump, err = DumpWorkspace(ctx, db, target.ID)
	case ScopeCrew:
		if len(target.CrewTargets) > 0 {
			dump, err = DumpCrew(ctx, db, target.CrewTargets[0].ID)
		}
	}
	if err != nil {
		_ = payloadWriter.Close()
		_ = payloadFile.Close()
		return nil, err
	}
	if dump != nil {
		if err := WriteDBSection(payloadWriter, dump, now); err != nil {
			_ = payloadWriter.Close()
			_ = payloadFile.Close()
			return nil, err
		}
	}
	if err := payloadWriter.Close(); err != nil {
		_ = payloadFile.Close()
		return nil, fmt.Errorf("backup: close payload tar: %w", err)
	}
	if err := payloadFile.Close(); err != nil {
		return nil, fmt.Errorf("backup: close payload file: %w", err)
	}

	// 6. Seal the payload (encrypt + hash) into a second temp file so
	// we know its size and SHA-256 before writing the outer bundle.
	// The sealed temp is streamed directly into the final .partial
	// output in step 8 without loading it into memory.
	sealedFile, err := os.CreateTemp("", "crewship-backup-sealed-*")
	if err != nil {
		return nil, fmt.Errorf("backup: create sealed temp: %w", err)
	}
	sealedPath := sealedFile.Name()
	defer func() { _ = os.Remove(sealedPath) }()

	rawPayload, err := os.Open(payloadPath)
	if err != nil {
		_ = sealedFile.Close()
		return nil, fmt.Errorf("backup: reopen payload: %w", err)
	}
	sha, sealedSize, err := SealPayload(sealedFile, rawPayload, WriteBundleOptions{
		Recipients: opts.Recipients,
		Passphrase: opts.Passphrase,
		NoEncrypt:  opts.NoEncrypt,
	})
	_ = rawPayload.Close()
	if err != nil {
		_ = sealedFile.Close()
		return nil, err
	}
	if err := sealedFile.Close(); err != nil {
		return nil, fmt.Errorf("backup: close sealed temp: %w", err)
	}

	// 7. Build the manifest with derived fields populated.
	manifest := &Manifest{
		FormatVersion:           FormatVersion,
		CrewshipVersionAtBackup: opts.CrewshipVersion,
		SchemaMigrationVersions: opts.SchemaMigrationVersions,
		Scope:                   opts.Scope,
		CompatibleTargets:       compatibleTargetsFor(opts.Scope),
		CreatedAt:               now,
		CreatedBy:               opts.Actor,
		SourceInstance:          currentInstance(),
		Contents:                buildContents(target),
		Checksums:               Checksums{PayloadSHA256: sha},
	}
	switch {
	case opts.NoEncrypt:
		manifest.Encryption = Encryption{Enabled: false}
	case len(opts.Recipients) > 0:
		manifest.Encryption = Encryption{Enabled: true, Algorithm: EncryptionAlgorithm}
		for _, r := range opts.Recipients {
			manifest.Encryption.Recipients = append(manifest.Encryption.Recipients, recipientString(r))
		}
	case opts.Passphrase != "":
		manifest.Encryption = Encryption{Enabled: true, Algorithm: EncryptionAlgorithm, KeyDerivation: "scrypt"}
	}

	// 8. Stream the outer bundle into .partial and atomic-rename.
	fname := BundleFileName(opts.Scope, target.Slug, now)
	if opts.Scope == ScopeCrew && len(target.CrewTargets) > 0 {
		fname = BundleFileName(opts.Scope, target.CrewTargets[0].Slug, now)
	}
	finalPath := filepath.Join(outDir, fname)
	partialPath := finalPath + ".partial"
	outFile, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: open partial: %w", err)
	}
	sealedIn, err := os.Open(sealedPath)
	if err != nil {
		_ = outFile.Close()
		_ = os.Remove(partialPath)
		return nil, fmt.Errorf("backup: reopen sealed: %w", err)
	}
	err = WriteBundleStream(outFile, manifest, sealedIn, sealedSize)
	_ = sealedIn.Close()
	if cerr := outFile.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(partialPath)
		return nil, err
	}
	info, err := os.Stat(partialPath)
	if err != nil {
		_ = os.Remove(partialPath)
		return nil, fmt.Errorf("backup: stat partial: %w", err)
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		_ = os.Remove(partialPath)
		return nil, fmt.Errorf("backup: rename final bundle: %w", err)
	}

	return &CreateResult{
		Path:     finalPath,
		Size:     info.Size(),
		SHA256:   manifest.Checksums.PayloadSHA256,
		Manifest: manifest,
	}, nil
}

// RestoreOptions collects the parameters for RestoreBackup.
type RestoreOptions struct {
	Path         string
	Passphrase   string
	Identities   []age.Identity
	AsWorkspace  string // optional slug override for workspace scope
	AsCrew       string // optional slug override for crew scope
	Actor        Actor
	DockerOps    DockerOps
	ContainerFor func(slug string) string // map crew slug -> container ID
}

// RestoreResult summarises what was restored.
type RestoreResult struct {
	Manifest     *Manifest
	RestoredWs   string
	CrewsCount   int
	RowsInserted int
}

// RestoreBackup applies a bundle to the target DB + docker engine. It
// does NOT recreate containers — the caller must provision them via
// the usual devcontainer path before calling this function, so the
// mount points exist and CopyTo has somewhere to land.
//
// In MVP this is gated to workspace / crew scope; instance scope
// produces ErrInvalidScope until PR 4 lands.
func RestoreBackup(ctx context.Context, db *sql.DB, opts RestoreOptions) (*RestoreResult, error) {
	if opts.Actor.UserID == "" {
		return nil, fmt.Errorf("backup: RestoreOptions.Actor.UserID required")
	}
	if err := RequireAdmin(opts.Actor.Role); err != nil {
		return nil, err
	}
	if opts.Path == "" {
		return nil, fmt.Errorf("backup: RestoreOptions.Path required")
	}

	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("backup: open bundle: %w", err)
	}
	defer func() { _ = f.Close() }()

	manifest, sealedReader, closeBundle, err := ReadBundleStream(f)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeBundle != nil {
			_ = closeBundle()
		}
	}()
	if manifest.Scope == ScopeInstance {
		return nil, fmt.Errorf("%w: instance scope restore is not supported yet (V1.5)", ErrInvalidScope)
	}

	// Wrap the sealed stream with a hashing reader so we can verify
	// the payload SHA-256 recorded in the manifest as we consume. The
	// verification happens at the end of extraction — a mismatch
	// surfaces as ErrInvalidChecksum and the caller must abort.
	hashed := NewHashingReader(sealedReader)

	// Decrypt payload if needed. The hasher sits on the SEALED bytes
	// (outside encryption) because that's what the writer hashed.
	var effectivePayload io.Reader = hashed
	if manifest.Encryption.Enabled {
		switch {
		case opts.Passphrase != "":
			r, err := DecryptStreamPassphrase(hashed, opts.Passphrase)
			if err != nil {
				return nil, err
			}
			effectivePayload = r
		case len(opts.Identities) > 0:
			r, err := DecryptStream(hashed, opts.Identities...)
			if err != nil {
				return nil, err
			}
			effectivePayload = r
		default:
			return nil, fmt.Errorf("backup: bundle is encrypted; supply Passphrase or Identities")
		}
	}

	// Extract sections. ExtractPayload consumes until EOF, which means
	// the hasher sees every sealed byte and can produce a final sum.
	extracted, err := ExtractPayload(effectivePayload)
	if err != nil {
		return nil, err
	}

	// Drain any trailer bytes the AGE reader may hold back, then
	// verify checksum. Mismatch means corruption or tampering and
	// aborts the restore before we touch the DB or docker volumes.
	_, _ = io.Copy(io.Discard, hashed)
	if err := VerifyChecksum(manifest.Checksums.PayloadSHA256, hashed.Sum()); err != nil {
		return nil, err
	}

	// Stage DB rewrites before any writes so both --as-* flags and the
	// FK rows land consistently.
	if extracted.DBDump != nil {
		if opts.AsWorkspace != "" {
			rewriteWorkspaceSlug(extracted.DBDump, opts.AsWorkspace)
		}
		if opts.AsCrew != "" && manifest.Scope == ScopeCrew && len(manifest.Contents.Crews) > 0 {
			rewriteCrewSlug(extracted.DBDump, manifest.Contents.Crews[0].ID, opts.AsCrew)
		}
	}

	// Commit the DB restore only after the Docker phase completes.
	// RestoreDumpTx runs the inserts inside a transaction and defers
	// the commit to preCommit, so a CopyTo failure leaves the target
	// DB untouched — no half-restored workspace rows with no volume
	// data behind them.
	dockerRestore := func(_ context.Context) error {
		if opts.DockerOps == nil || opts.ContainerFor == nil {
			return nil
		}
		for _, c := range manifest.Contents.Crews {
			containerID := opts.ContainerFor(c.Slug)
			if containerID == "" {
				continue
			}
			if err := RestoreCrew(ctx, opts.DockerOps, containerID, c.Slug, extracted); err != nil {
				return fmt.Errorf("backup: restore crew %s: %w", c.Slug, err)
			}
		}
		return nil
	}
	var stats RestoreStats
	if extracted.DBDump != nil {
		s, err := RestoreDumpTx(ctx, db, extracted.DBDump, dockerRestore)
		if err != nil {
			return nil, err
		}
		stats = s
	} else {
		if err := dockerRestore(ctx); err != nil {
			return nil, err
		}
	}

	// No-op restore detection: the bundle carried rows but every one
	// of them collided with an existing primary key and INSERT OR
	// IGNORE silently dropped it. The classic cause is "restore into
	// the same instance that produced the bundle" — the admin thinks
	// they rolled state back but nothing changed. Surface a loud
	// warning via a dedicated error so CLI + API both show it.
	if stats.RowsSeen > 0 && stats.RowsInserted == 0 {
		return &RestoreResult{
			Manifest:     manifest,
			RestoredWs:   firstWorkspaceSlug(extracted.DBDump),
			CrewsCount:   len(manifest.Contents.Crews),
			RowsInserted: 0,
		}, fmt.Errorf("backup: restore completed but inserted 0 of %d rows — every primary key collided with an existing row. Restore into a clean target instance, or supply --as-workspace to re-scope IDs (workspace scope only)", stats.RowsSeen)
	}

	return &RestoreResult{
		Manifest:     manifest,
		RestoredWs:   firstWorkspaceSlug(extracted.DBDump),
		CrewsCount:   len(manifest.Contents.Crews),
		RowsInserted: stats.RowsInserted,
	}, nil
}

// ListBackups returns metadata for every bundle currently in dir. The
// result is stable-ordered by CreatedAt descending so the newest
// bundle is first (matches what most users want in the CLI output).
func ListBackups(dir string) ([]ListEntry, error) {
	entries, err := os.ReadDir(dir)
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
		m, inspectErr := Inspect(path)
		le := ListEntry{
			Path: path,
			Size: info.Size(),
		}
		if inspectErr == nil && m != nil {
			le.Scope = m.Scope
			le.Encrypted = m.Encryption.Enabled
			le.CreatedAt = m.CreatedAt
			le.FormatVersion = m.FormatVersion
		}
		out = append(out, le)
	}
	return out, nil
}

// ListEntry is the row format emitted by ListBackups and surfaced by
// the CLI `crewship backup list` output.
type ListEntry struct {
	Path          string
	Size          int64
	Scope         Scope
	Encrypted     bool
	CreatedAt     time.Time
	FormatVersion int
}

// Inspect opens a bundle and returns its manifest without decrypting
// the payload. Used by `crewship backup inspect` and ListBackups.
func Inspect(path string) (*Manifest, error) {
	f, err := os.Open(path)
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

// Delete removes a bundle. Before touching disk it verifies the file
// is actually a Crewship backup — the name ends with .tar.zst AND the
// outer tar yields a valid MANIFEST.json. This guard prevents a
// mis-click from using the delete endpoint as a generic rm primitive
// for anything that passed API-level path validation.
//
// Callers emit an audit log row after a successful delete.
func Delete(path string) error {
	if !strings.HasSuffix(path, ".tar.zst") {
		return fmt.Errorf("backup: refuse to delete %q: not a .tar.zst bundle", path)
	}
	if _, err := Inspect(path); err != nil {
		return fmt.Errorf("backup: refuse to delete %q: failed inspect (%v)", path, err)
	}
	if err := os.Remove(path); err != nil {
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
	return fmt.Errorf("backup refused: agent %q is running; abort the run or wait for it to finish", running)
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

// compatibleTargetsFor returns the list of TargetX values that go into
// the manifest's compatible_targets slice. Crew bundles are only
// same-instance (FK / skills references); workspace and instance are
// any-instance.
func compatibleTargetsFor(s Scope) []Target {
	switch s {
	case ScopeCrew:
		return []Target{TargetSameInstance}
	default:
		return []Target{TargetAnyInstance}
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

// buildContents translates a WorkspaceTarget into the manifest's
// Contents summary. We mark every section as "included" for every
// crew because CollectCrew streams all of them; skipping would require
// per-crew introspection which is not useful in MVP.
func buildContents(t *WorkspaceTarget) Contents {
	contents := Contents{
		Workspace: &WorkspaceSummary{
			ID:   t.ID,
			Slug: t.Slug,
			Name: t.Name,
		},
	}
	for _, c := range t.CrewTargets {
		contents.Crews = append(contents.Crews, CrewSummary{
			ID:                         c.ID,
			Slug:                       c.Slug,
			Name:                       c.Name,
			RuntimeImage:               c.RuntimeImage,
			BaseImageDigest:            c.BaseImageDigest,
			CachedImageDigest:          c.CachedImageDigest,
			ConfigHash:                 c.ConfigHash,
			DevcontainerConfigIncluded: c.DevcontainerConfig != "",
			MiseConfigIncluded:         c.MiseConfig != "",
			WorkspaceIncluded:          c.ContainerID != "",
			VolumesIncluded:            []string{"home", "tools"},
			MemoryIncluded:             c.ContainerID != "",
			AgentCount:                 c.AgentCount,
		})
	}
	return contents
}

// rewriteWorkspaceSlug updates the single workspace row in the dump so
// a restore with --as-workspace <slug> lands under the new identity.
// It does NOT change the workspace ID (primary key) — callers that
// want a new ID regenerate one before insert. We only change the slug
// + name so the restored ws does not collide on the UNIQUE(slug).
func rewriteWorkspaceSlug(dump *DBDump, newSlug string) {
	rows, ok := dump.Tables["workspaces"]
	if !ok || len(rows) == 0 {
		return
	}
	rows[0]["slug"] = newSlug
	rows[0]["name"] = newSlug
}

// rewriteCrewSlug does the equivalent for crew-scope restores.
func rewriteCrewSlug(dump *DBDump, crewID, newSlug string) {
	rows, ok := dump.Tables["crews"]
	if !ok {
		return
	}
	for _, r := range rows {
		if r["id"] == crewID {
			r["slug"] = newSlug
			r["name"] = newSlug
			return
		}
	}
}

// firstWorkspaceSlug returns the slug of the first (and typically only)
// workspace row in the dump, or "" if none present.
func firstWorkspaceSlug(dump *DBDump) string {
	if dump == nil {
		return ""
	}
	rows, ok := dump.Tables["workspaces"]
	if !ok || len(rows) == 0 {
		return ""
	}
	if s, ok := rows[0]["slug"].(string); ok {
		return s
	}
	return ""
}
