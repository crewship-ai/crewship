package backup

// CreateOptions / CreateResult / CreateBackup and their private
// helpers. Split from runner.go so the create-side flow (validate →
// resolve → lock → collect → seal → write bundle) is self-contained;
// restore-side code lives in runner_restore.go and the other runner
// helpers (list/inspect/verify/delete/rotate + shared utilities)
// stay in runner.go.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"filippo.io/age"
)

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
	// Storage overrides the file-system operations used for bundle
	// output. Nil uses LocalStorageOps; tests can inject an in-memory
	// or S3-backed implementation via package-level SetDefaultStorage
	// or this field.
	Storage StorageOps
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
	// Reject conflicting encryption modes up front. Letting a bad combo
	// (e.g. Passphrase + Recipients) slip past here means it fails
	// later, after we've already acquired the lock and written a
	// .partial, which leaves cleanup on the caller. Exactly one of the
	// three must be set.
	modes := 0
	if o.Passphrase != "" {
		modes++
	}
	if len(o.Recipients) > 0 {
		modes++
	}
	if o.NoEncrypt {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("backup: exactly one of Passphrase, Recipients, or NoEncrypt=true must be set")
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
func CreateBackup(ctx context.Context, db *sql.DB, opts CreateOptions) (result *CreateResult, retErr error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	st := resolveStorage(opts.Storage)

	// Observability: capture duration + classify outcome regardless of
	// which return path fires. The deferred hook records bytes from the
	// successful result (or 0 on failure) and emits the outbound webhook
	// so downstream consumers see create.success / create.failed events.
	finish := ObserveCreateStart(string(opts.Scope))
	// Close over target so crew-scope events carry a workspace id too —
	// LoadCrewTarget resolves it even when opts.WorkspaceID is empty.
	var target *WorkspaceTarget
	defer func() {
		var bytes int64
		var sha, path string
		workspaceID := opts.WorkspaceID
		if result != nil {
			bytes = result.Size
			sha = result.SHA256
			path = result.Path
		}
		if workspaceID == "" && target != nil {
			workspaceID = target.ID
		}
		finish(retErr, bytes)
		cfg := WebhookConfigFromEnv()
		event := "backup.created"
		errStr := ""
		if retErr != nil {
			event = "backup.failed"
			errStr = retErr.Error()
		}
		SendEventAsync(cfg, WebhookEvent{
			Event:       event,
			Timestamp:   time.Now().UTC(),
			WorkspaceID: workspaceID,
			Scope:       string(opts.Scope),
			Path:        path,
			Bytes:       bytes,
			SHA256:      sha,
			Error:       errStr,
		}, nil)
	}()

	// 1. Resolve targets. `target` is declared in the deferred webhook
	// block above so crew-scope events can populate WorkspaceID from
	// the resolved target.ID.
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
	ObserveLockAcquired(target.ID)
	defer func() {
		_ = release(context.Background())
		ObserveLockReleased(target.ID)
	}()

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
		outDir, err = defaultBackupsDirFor(st)
		if err != nil {
			return nil, err
		}
	}
	if err := st.MkdirAll(ctx, outDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: ensure output dir: %w", err)
	}
	// Sweep stale .partial files older than one hour. A process that
	// crashed mid-CreateBackup leaves one behind; without this sweep
	// the admin accumulates orphans forever and disk fills up.
	cleanupStalePartials(ctx, st, outDir, time.Hour)

	// 5. Build the payload tar to a temp file so peak memory is bounded
	// by the zstd encoder's window (a few MB) rather than the full
	// workspace size. A multi-GB workspace therefore stays within
	// reasonable RAM even on modest hosts.
	now := time.Now().UTC()
	payloadFile, err := st.CreateTemp(ctx, "", "crewship-backup-payload-*.tar.zst")
	if err != nil {
		return nil, fmt.Errorf("backup: create payload temp: %w", err)
	}
	payloadPath := payloadFile.Name()
	// Cleanup must run even if the request ctx is cancelled — we
	// still need to remove the temp file, otherwise a client
	// disconnect leaks GBs of staging data.
	defer func() { _ = st.Remove(context.Background(), payloadPath) }()

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
	sealedFile, err := st.CreateTemp(ctx, "", "crewship-backup-sealed-*")
	if err != nil {
		return nil, fmt.Errorf("backup: create sealed temp: %w", err)
	}
	sealedPath := sealedFile.Name()
	defer func() { _ = st.Remove(context.Background(), sealedPath) }()

	rawPayload, err := st.Open(ctx, payloadPath)
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
	outFile, err := st.Create(ctx, partialPath, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: open partial: %w", err)
	}
	sealedIn, err := st.Open(ctx, sealedPath)
	if err != nil {
		_ = outFile.Close()
		_ = st.Remove(context.Background(), partialPath)
		return nil, fmt.Errorf("backup: reopen sealed: %w", err)
	}
	err = WriteBundleStream(outFile, manifest, sealedIn, sealedSize)
	_ = sealedIn.Close()
	if cerr := outFile.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = st.Remove(context.Background(), partialPath)
		return nil, err
	}
	info, err := st.Stat(ctx, partialPath)
	if err != nil {
		_ = st.Remove(context.Background(), partialPath)
		return nil, fmt.Errorf("backup: stat partial: %w", err)
	}
	if err := st.Rename(ctx, partialPath, finalPath); err != nil {
		_ = st.Remove(context.Background(), partialPath)
		return nil, fmt.Errorf("backup: rename final bundle: %w", err)
	}

	return &CreateResult{
		Path:     finalPath,
		Size:     info.Size(),
		SHA256:   manifest.Checksums.PayloadSHA256,
		Manifest: manifest,
	}, nil
}

// compatibleTargetsFor returns the Target set recorded in the
// manifest's compatible_targets slice. Crew bundles are only
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

// buildContents assembles the manifest's Contents summary. We mark
// every section as "included" for every crew because CollectCrew
// streams all of them; skipping would require per-crew introspection
// which is not useful in MVP.
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
