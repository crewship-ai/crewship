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
	"strings"
	"time"

	"filippo.io/age"
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

	// 2a. Acquire the in-process workspace guard BEFORE the DB lock.
	// This closes the TOCTOU race with mission-start: without it, a
	// request already past refuseIfBackupInProgress could register a
	// new agent run between our DB lock insert and ensureAgentsIdle,
	// silently missing from the dump. See internal/backup/guard.go.
	guardRelease, err := DefaultGuard().BeginBackup(target.ID)
	if err != nil {
		return nil, err
	}
	defer guardRelease()

	// 2b. Acquire DB advisory lock (per-workspace). The DB row is the
	// durable, user-visible status marker (`crewship backup status`
	// reads it) and the multi-process-safety net.
	lockMgr := NewSQLLockManager(db)
	release, err := lockMgr.AcquireWorkspaceLock(ctx, target.ID, opts.Actor.UserID, LockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release(context.Background()) }()

	// 3. Agent idle guard — refuse if any agent is actively running.
	// With the in-process guard already held, no new mission can slip
	// in between this check and the payload build.
	if err := ensureAgentsIdle(ctx, db, target); err != nil {
		return nil, err
	}

	// 4. Output directory. Supports --output via opts.OutputDir and
	// falls back to ~/.crewship/backups. The preflight (disk-space
	// check, partial-file cleanup) operates on the effective path so
	// a non-default --output is not left with stale .partial files.
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
	// Sweep stale .partial files older than one hour. A process that
	// crashed mid-CreateBackup leaves one behind; without this sweep
	// the admin accumulates orphans forever and disk fills up.
	cleanupStalePartials(outDir, time.Hour)

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

	// 7. Build the manifest with derived fields populated. Version and
	// migration-version fields fall back to runtime detection so the
	// resulting manifest is never empty in those slots.
	migrations := opts.SchemaMigrationVersions
	if len(migrations) == 0 {
		migrations = AppliedMigrationVersions(ctx, db)
	}
	manifest := &Manifest{
		FormatVersion:           FormatVersion,
		CrewshipVersionAtBackup: DetectCrewshipVersion(opts.CrewshipVersion),
		SchemaMigrationVersions: migrations,
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
	// DryRun, when true, runs every validation (checksum, schema-skew,
	// decrypt, payload walk) but commits no DB writes and performs no
	// docker CopyTo. RestoreResult reports what WOULD have happened.
	DryRun bool
	// Logger, if set, receives human-readable progress/warning
	// messages (e.g. "docker phase skipped …"). The CLI wires this
	// into stderr; the REST handler can log to slog.
	Logger func(string)
}

// RestoreResult summarises what was restored.
type RestoreResult struct {
	Manifest            *Manifest
	RestoredWs          string
	RestoredWorkspaceID string // new CUID when --as-workspace remapped IDs
	CrewsCount          int
	RowsInserted        int
	DockerPhaseSkipped  bool
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

	// Schema skew detection. The bundle records which DB migrations
	// had been applied on the source; the target might be newer (OK —
	// migrations are additive), or older (NOT OK — missing columns
	// would silently drop on INSERT because RestoreDumpTx skips
	// unknown columns). Fail loudly rather than silently corrupting
	// a restore.
	if len(manifest.SchemaMigrationVersions) > 0 {
		applied := AppliedMigrationVersions(ctx, db)
		appliedSet := map[int]bool{}
		for _, v := range applied {
			appliedSet[v] = true
		}
		var missing []int
		for _, v := range manifest.SchemaMigrationVersions {
			if !appliedSet[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf(
				"%w — missing migrations %v. Upgrade Crewship on this host to at least the version that introduced those migrations, then retry restore",
				ErrSchemaTooOld, missing,
			)
		}
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
	// Large per-crew sections live in temp files owned by the returned
	// ExtractedPayload — Close must fire on every exit path to clean
	// them up.
	extracted, err := ExtractPayload(effectivePayload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = extracted.Close() }()

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
		// When the admin picked a new slug via --as-* they want the
		// restored data to live alongside the source. Regenerate every
		// primary key and rewrite every FK so INSERT OR IGNORE does
		// not drop the whole bundle on PK collision.
		if opts.AsWorkspace != "" || opts.AsCrew != "" {
			if err := RemapIDs(ctx, db, extracted.DBDump); err != nil {
				return nil, err
			}
		}
	}

	// Commit the DB restore only after the Docker phase completes.
	// RestoreDumpTx runs the inserts inside a transaction and defers
	// the commit to preCommit, so a CopyTo failure leaves the target
	// DB untouched — no half-restored workspace rows with no volume
	// data behind them.
	//
	// When the admin picked --as-workspace / --as-crew we SKIP the
	// Docker phase entirely. manifest.Contents.Crews carries the
	// ORIGINAL slugs, so ContainerFor(slug) would resolve to the
	// source crew's containers — CopyTo would then clobber the
	// original workspace's live container data. The new crews do not
	// yet have provisioned containers anyway (their DB rows were only
	// just inserted in this very transaction). Admins restoring under
	// a new slug therefore get DB rows only; they must provision the
	// new crews via `crewship crew provision` and then re-run restore
	// without --as-* to land the container state.
	skipDocker := opts.AsWorkspace != "" || opts.AsCrew != ""
	dockerRestore := func(_ context.Context) error {
		if skipDocker {
			if opts.Logger != nil {
				opts.Logger("docker phase skipped because --as-workspace / --as-crew was supplied; provision the new crews and re-run restore without the rewrite flag to land container state")
			}
			return nil
		}
		if opts.DockerOps == nil || opts.ContainerFor == nil {
			return nil
		}
		// Preflight: every target container must exist before we start
		// writing. Without this, a missing crew container surfaces as
		// "No such container" from deep inside CopyTo, after other
		// crews have already been mutated — partial restore state.
		for _, c := range manifest.Contents.Crews {
			containerID := opts.ContainerFor(c.Slug)
			if containerID == "" {
				continue
			}
			exists, err := opts.DockerOps.ContainerExists(ctx, containerID)
			if err != nil {
				return fmt.Errorf("backup: preflight crew %s: %w", c.Slug, err)
			}
			if !exists {
				return fmt.Errorf("backup: crew %q container %q is not provisioned on this instance; run `crewship crew provision %s` then re-run restore", c.Slug, containerID, c.Slug)
			}
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
	// Dry-run short-circuit: all validation already ran (manifest
	// parse, checksum verify, payload extract, schema-skew). Nothing
	// left mutates state, so return early with a synthetic success
	// result that reports what would have been inserted.
	if opts.DryRun {
		if opts.Logger != nil {
			opts.Logger("dry-run: checksum + schema compat OK; no DB or docker writes performed")
		}
		rowsSeen := 0
		if extracted.DBDump != nil {
			for _, rows := range extracted.DBDump.Tables {
				rowsSeen += len(rows)
			}
		}
		return &RestoreResult{
			Manifest:            manifest,
			RestoredWs:          firstWorkspaceSlug(extracted.DBDump),
			RestoredWorkspaceID: firstWorkspaceID(extracted.DBDump),
			CrewsCount:          len(manifest.Contents.Crews),
			RowsInserted:        rowsSeen, // dry-run reports potential inserts
			DockerPhaseSkipped:  skipDocker,
		}, nil
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
		}, fmt.Errorf("%w: 0 of %d rows inserted — every primary key collided with an existing row. Restore into a clean target instance, or supply --as-workspace to re-scope IDs (workspace scope only)", ErrNoOpRestore, stats.RowsSeen)
	}

	return &RestoreResult{
		Manifest:            manifest,
		RestoredWs:          firstWorkspaceSlug(extracted.DBDump),
		RestoredWorkspaceID: firstWorkspaceID(extracted.DBDump),
		CrewsCount:          len(manifest.Contents.Crews),
		RowsInserted:        stats.RowsInserted,
		DockerPhaseSkipped:  skipDocker,
	}, nil
}

// firstWorkspaceID returns the "id" of the first workspace row in
// the dump — after RemapIDs this is the freshly generated CUID, so
// the CLI / audit log can surface it back to the admin.
func firstWorkspaceID(dump *DBDump) string {
	if dump == nil {
		return ""
	}
	rows, ok := dump.Tables["workspaces"]
	if !ok || len(rows) == 0 {
		return ""
	}
	if s, ok := rows[0]["id"].(string); ok {
		return s
	}
	return ""
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
			if m.Contents.Workspace != nil {
				le.WorkspaceID = m.Contents.Workspace.ID
			}
		}
		out = append(out, le)
	}
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
	Encrypted     bool
	CreatedAt     time.Time
	FormatVersion int
	WorkspaceID   string
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
func Verify(path string) (*VerifyResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("backup: stat %s: %w", path, err)
	}

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
func Rotate(dir, workspaceID string, keepLast int, keepDays int, dryRun bool) ([]string, error) {
	entries, err := ListBackups(dir)
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
		if err := Delete(p); err != nil {
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

// cleanupStalePartials removes *.partial files older than maxAge in
// dir. A crashed or cancelled CreateBackup leaves one behind; without
// this sweep they accumulate forever. Errors are swallowed — the only
// consequence of a failed cleanup is a file that will be retried on
// the next backup, not a correctness issue.
func cleanupStalePartials(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
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
