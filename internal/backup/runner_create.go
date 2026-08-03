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
	"log/slog"
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
	// Level selects which per-crew sections the collector includes.
	// Empty resolves to DefaultScopeLevel (standard) so existing CLI
	// / API callers that don't yet know about presets keep producing
	// the same bundles they did before.
	Level ScopeLevel
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
	// CrewContainerName maps a crew (id, slug) to a Docker container name
	// (the provider owns this). The id is required so names stay globally
	// unique across tenants (audit C1). Nil is valid for tests.
	CrewContainerName func(id, slug string) string
	// DockerOps executes pause/unpause/CopyFrom against the daemon.
	DockerOps DockerOps
	// Storage overrides the file-system operations used for bundle
	// output. Nil uses LocalStorageOps; tests can inject an in-memory
	// or S3-backed implementation via package-level SetDefaultStorage
	// or this field.
	Storage StorageOps
	// BlobRoot is the content-addressed memory-version blob directory
	// on THIS (source) instance — {MemoryRoot}/versions, the same path
	// RecordVersion writes under (internal/memory/versions.go). When
	// set, CreateBackup collects the blob referenced by every
	// memory_versions row the DB dump carries into a dedicated bundle
	// section so a restore can bring back readable memory history
	// instead of DB rows pointing at nothing. Empty disables the
	// section — matches the rest of the memory subsystem's "empty
	// BlobRoot disables versioning" convention.
	BlobRoot string
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

	// 2. Reconcile each crew's ContainerID against the Docker daemon.
	// LoadX populates ContainerID purely from the slug→name function,
	// which means crews that have never been provisioned (no container
	// ever created) still get an ID. Trying to pause a non-existent
	// container later produces the cryptic "No such container" error
	// instead of a clean skip — and that fails the WHOLE backup, not
	// just the missing crew. Probe upfront and clear ContainerID for
	// any crew whose container doesn't exist; CollectCrew already
	// no-ops on empty ContainerID, so DB rows still backup correctly
	// while the live filesystem section is simply absent for those
	// crews. Probe is cheap (single Docker /containers/$name/json
	// call per crew) and runs in serial — workspaces with hundreds of
	// crews would want batching, but that's not a v1 concern.
	if opts.DockerOps != nil {
		for i := range target.CrewTargets {
			c := &target.CrewTargets[i]
			if c.ContainerID == "" {
				continue
			}
			exists, exErr := opts.DockerOps.ContainerExists(ctx, c.ContainerID)
			if exErr != nil {
				// Daemon hiccup / permission error — fail loudly. The
				// previous behaviour silently zeroed ContainerID and
				// kept going, which produced "successful" bundles
				// where every crew's filesystem was missing — much
				// worse than a clear backup error the operator can
				// retry. Only the definite-absent case (exists=false
				// below) skips the section.
				return nil, fmt.Errorf("backup: probe container %s for crew %s: %w", c.ContainerID, c.Slug, exErr)
			}
			if !exists {
				c.ContainerID = ""
			}
		}
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
	// Scope the sweep to THIS workspace/crew slug so a concurrent
	// long-running backup of a sibling tenant whose .partial happens
	// to be >1h old isn't deleted out from under it.
	cleanupSlug := target.Slug
	if opts.Scope == ScopeCrew && len(target.CrewTargets) > 0 {
		cleanupSlug = target.CrewTargets[0].Slug
	}
	cleanupStalePartials(ctx, st, outDir, cleanupSlug, time.Hour)

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
	level := opts.Level
	if !level.Valid() {
		level = DefaultScopeLevel
	}
	captures := map[string]CrewCapture{}
	for _, crew := range target.CrewTargets {
		if opts.DockerOps != nil && crew.ContainerID != "" {
			capture, err := CollectCrew(ctx, opts.DockerOps, payloadWriter, crew, level)
			if err != nil {
				_ = payloadWriter.Close()
				_ = payloadFile.Close()
				return nil, err
			}
			captures[crew.Slug] = capture
			// A crew whose container exists but whose memory tree came
			// back empty is the shape of #1713 recurring — a path that
			// stopped resolving, a mount that stopped being mounted.
			// The manifest will say so honestly either way; this is the
			// breadcrumb for whoever has to work out why.
			if capture.CrewEntries == 0 {
				slog.Warn("backup: crew has a running container but no memory tree was captured",
					"crew", crew.Slug, "path", ContainerCrewPath, "workspace_id", target.ID)
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

	// 5d. Memory-version blobs referenced by the DB dump's
	// memory_versions rows. Must run AFTER the dump is built (it reads
	// dump.Tables["memory_versions"]) and BEFORE the payload tar is
	// closed. See memoryblobs.go for why this section exists at all —
	// memory_versions rode every workspace bundle already; the blob
	// files it points at did not.
	var memoryBlobsResult *MemoryBlobsResult
	if dump != nil {
		memoryBlobsResult, err = WriteMemoryBlobsSection(payloadWriter, opts.BlobRoot, dump, now)
		if err != nil {
			_ = payloadWriter.Close()
			_ = payloadFile.Close()
			return nil, err
		}
		if len(memoryBlobsResult.Missing) > 0 {
			// Not fatal — see WriteMemoryBlobsSection doc comment. Still
			// worth a loud breadcrumb: a memory_versions row with no
			// blob on disk is exactly the shape of the bug this section
			// exists to prevent from recurring.
			slog.Warn("backup: memory_versions rows reference blobs missing on disk",
				"count", len(memoryBlobsResult.Missing), "workspace_id", target.ID)
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
	contents := buildContents(target, level, captures)
	if memoryBlobsResult != nil {
		contents.MemoryBlobsIncluded = memoryBlobsResult.Included
		contents.MemoryBlobsMissing = len(memoryBlobsResult.Missing)
	}
	manifest := &Manifest{
		FormatVersion:           FormatVersion,
		CrewshipVersionAtBackup: DetectCrewshipVersion(opts.CrewshipVersion),
		SchemaMigrationVersions: migrations,
		Scope:                   opts.Scope,
		ScopeLevel:              level,
		CompatibleTargets:       compatibleTargetsFor(opts.Scope),
		CreatedAt:               now,
		CreatedBy:               opts.Actor,
		SourceInstance:          currentInstance(),
		Contents:                contents,
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

// buildContents assembles the manifest's Contents summary from what
// CollectCrew reported writing, keyed by crew slug.
//
// It used to predict instead: every per-section flag was set from
// `c.ContainerID != ""`, so the manifest described the bundle the
// collector was supposed to have produced rather than the one it did.
// That is how #1713 stayed invisible for as long as it did — the
// collector was reading /output and calling it memory, and the manifest
// dutifully reported memory_included: true on every bundle. A predicted
// flag cannot notice a wrong path; an observed one cannot miss it.
//
// The prediction was also load-bearing in the other direction: the
// restore docker phase gates on these flags, so a false positive turns
// into a hard "crew has filesystem data but is not provisioned" failure
// for a crew that has no data at all.
//
// Crews with no capture entry (no container, or docker ops absent) get
// zeroes, which is what a crew with nothing on disk should report.
func buildContents(t *WorkspaceTarget, level ScopeLevel, captures map[string]CrewCapture) Contents {
	contents := Contents{
		Workspace: &WorkspaceSummary{
			ID:   t.ID,
			Slug: t.Slug,
			Name: t.Name,
		},
	}
	for _, c := range t.CrewTargets {
		capture := captures[c.Slug]
		summary := CrewSummary{
			ID:                         c.ID,
			Slug:                       c.Slug,
			Name:                       c.Name,
			RuntimeImage:               c.RuntimeImage,
			BaseImageDigest:            c.BaseImageDigest,
			CachedImageDigest:          c.CachedImageDigest,
			ConfigHash:                 c.ConfigHash,
			DevcontainerConfigIncluded: c.DevcontainerConfig != "",
			MiseConfigIncluded:         c.MiseConfig != "",
			WorkspaceIncluded:          capture.WorkspaceEntries > 0,
			MemoryIncluded:             capture.CrewEntries > 0,
			OutputIncluded:             capture.OutputEntries > 0,
			VolumesIncluded:            capture.Volumes(),
			SystemIncluded:             capture.VarLibEntries > 0,
			AgentCount:                 c.AgentCount,
		}
		contents.Crews = append(contents.Crews, summary)
	}
	return contents
}

// rewriteWorkspaceSlug updates the single workspace row in the dump so
// a restore with --as-workspace <slug> lands under the new identity.
// It does NOT change the workspace ID (primary key) — callers that
// want a new ID regenerate one before insert. We only change the slug
