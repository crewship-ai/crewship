package backup

// RestoreOptions / RestoreResult / RestoreBackup and the slug/ID
// rewriting helpers plus replayRestoreBackfills. Split from runner.go
// so the restore-side flow (open bundle → verify checksum → decrypt
// → extract payload → rewrite IDs → apply DB dump → replay schema
// backfills → docker phase) is self-contained.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
)

// RestoreOptions collects the parameters for RestoreBackup.
type RestoreOptions struct {
	Path         string
	Passphrase   string
	Identities   []age.Identity
	AsWorkspace  string // optional slug override for workspace scope
	AsCrew       string // optional slug override for crew scope
	Actor        Actor
	DockerOps    DockerOps
	ContainerFor func(id, slug string) string // map crew (id, slug) -> container ID
	// DryRun, when true, runs every validation (checksum, schema-skew,
	// decrypt, payload walk) but commits no DB writes and performs no
	// docker CopyTo. RestoreResult reports what WOULD have happened.
	DryRun bool
	// Replace, when true, deletes every existing workspace-scoped row
	// on the target whose workspace matches the bundle by either id or
	// slug BEFORE the INSERT pass. This is the canonical
	// disaster-recovery path: a post-`dev.sh nuke` bootstrap
	// regenerates the workspace CUID with the same slug, and a normal
	// restore would either no-op (id collision against fresh empty row)
	// or fail UNIQUE(slug). --replace clears the conflicting target
	// state so the bundle lands with its original IDs intact.
	//
	// Mutually exclusive with AsWorkspace / AsCrew — those flags exist
	// to FORK the workspace under a new slug; --replace exists to
	// REASSERT the bundle's identity over whatever the target has.
	Replace bool
	// Logger, if set, receives human-readable progress/warning
	// messages (e.g. "docker phase skipped …"). The CLI wires this
	// into stderr; the REST handler can log to slog.
	Logger func(string)
	// Storage overrides file-system operations used while reading the
	// bundle. Nil uses LocalStorageOps (see CreateOptions.Storage for
	// the rationale).
	Storage StorageOps
}

// RestoreResult summarises what was restored.
type RestoreResult struct {
	Manifest            *Manifest
	RestoredWs          string
	RestoredWorkspaceID string // new CUID when --as-workspace remapped IDs
	CrewsCount          int
	RowsInserted        int
	DockerPhaseSkipped  bool
	// DroppedCrewFilesystems carries the slugs of crews whose bundle
	// section included filesystem data (workspace / memory / system
	// paths) that this restore did NOT land — typically because the
	// caller supplied --as-workspace or --as-crew, which forces a
	// docker-phase skip to avoid clobbering the source's still-live
	// containers. Empty for a clean full-fidelity restore; non-empty
	// means an admin should treat the operation as DB-rows-only and
	// either provision matching crews then re-run restore without the
	// rewrite flag, or accept the loss explicitly.
	//
	// Surfacing this as structured state (vs the Logger-callback path
	// only) is on purpose — when an API handler passes a nil Logger
	// the old path silently dropped data.
	DroppedCrewFilesystems []string
}

// RestoreBackup applies a bundle to the target DB + docker engine. It
// does NOT recreate containers — the caller must provision them via
// the usual devcontainer path before calling this function, so the
// mount points exist and CopyTo has somewhere to land.
//
// In MVP this is gated to workspace / crew scope; instance scope is
// rejected up-front because it requires cross-workspace orchestration
// that lives outside this package.
func RestoreBackup(ctx context.Context, db *sql.DB, opts RestoreOptions) (result *RestoreResult, retErr error) {
	if opts.Actor.UserID == "" {
		return nil, fmt.Errorf("backup: RestoreOptions.Actor.UserID required")
	}
	if err := RequireAdmin(opts.Actor.Role); err != nil {
		return nil, err
	}
	if opts.Path == "" {
		return nil, fmt.Errorf("backup: RestoreOptions.Path required")
	}
	st := resolveStorage(opts.Storage)

	// Manifest metadata captured as the restore progresses, so the
	// webhook can report scope/workspace even on failure paths that
	// abort before `result` is populated. Updated right after
	// ReadBundleStream parses the manifest below.
	var (
		manifestScope       string
		manifestWorkspaceID string
	)
	// Observability: classify outcome regardless of return path. Do NOT
	// observe a DryRun — it is not a "real" restore and would skew the
	// restored_total counter.
	defer func() {
		if opts.DryRun {
			return
		}
		ObserveRestore(retErr)
		cfg := WebhookConfigFromEnv()
		event := "backup.restored"
		errStr := ""
		scope := manifestScope
		workspaceID := manifestWorkspaceID
		if result != nil && result.Manifest != nil {
			scope = string(result.Manifest.Scope)
			if ws := result.Manifest.Contents.Workspace; ws != nil {
				workspaceID = ws.ID
			}
		}
		// result.RestoredWorkspaceID takes precedence when the admin
		// used --as-workspace — that's the NEW id after RemapIDs, not
		// the one the bundle carried.
		if result != nil && result.RestoredWorkspaceID != "" {
			workspaceID = result.RestoredWorkspaceID
		}
		if retErr != nil {
			event = "backup.failed"
			errStr = retErr.Error()
		}
		SendEventAsync(cfg, WebhookEvent{
			Event:       event,
			Timestamp:   time.Now().UTC(),
			WorkspaceID: workspaceID,
			Scope:       scope,
			Path:        opts.Path,
			Error:       errStr,
		}, nil)
	}()

	f, err := st.Open(ctx, opts.Path)
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
	// Capture manifest metadata so the deferred webhook above can
	// still emit scope + workspace on failure paths that never reach
	// a successful `result`.
	manifestScope = string(manifest.Scope)
	if ws := manifest.Contents.Workspace; ws != nil {
		manifestWorkspaceID = ws.ID
	}
	if manifest.Scope == ScopeInstance {
		return nil, fmt.Errorf("%w: instance scope restore is not supported yet (V1.5)", ErrInvalidScope)
	}

	// Enforce that --as-workspace / --as-crew match the bundle scope
	// BEFORE we start rewriting IDs. Without this the CLI can point
	// --as-crew at a workspace bundle (silently ignored, admin confused
	// why nothing happened) or --as-workspace at a crew bundle (rewrites
	// the workspace row even though the restore is scoped to a single
	// crew). Both are wrong, neither triggers a useful error later, so
	// fail loudly here.
	if opts.AsWorkspace != "" && opts.AsCrew != "" {
		return nil, fmt.Errorf("%w: supply only one of --as-workspace or --as-crew", ErrInvalidScope)
	}
	if opts.AsWorkspace != "" && manifest.Scope != ScopeWorkspace {
		return nil, fmt.Errorf("%w: --as-workspace is only valid for workspace-scope bundles (this bundle is %s)", ErrInvalidScope, manifest.Scope)
	}
	if opts.AsCrew != "" && manifest.Scope != ScopeCrew {
		return nil, fmt.Errorf("%w: --as-crew is only valid for crew-scope bundles (this bundle is %s)", ErrInvalidScope, manifest.Scope)
	}
	// --replace and --as-* are semantic opposites: --replace reasserts
	// the bundle's identity OVER whatever the target has under the
	// same slug; --as-* forks the bundle under a NEW slug. Combining
	// them is incoherent — refuse up front so the admin sees the
	// conflict before we touch anything.
	if opts.Replace && (opts.AsWorkspace != "" || opts.AsCrew != "") {
		return nil, fmt.Errorf("%w: --replace is incompatible with --as-workspace / --as-crew", ErrInvalidScope)
	}
	if opts.Replace && manifest.Scope != ScopeWorkspace {
		return nil, fmt.Errorf("%w: --replace is only supported for workspace-scope bundles", ErrInvalidScope)
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
	extracted, err := ExtractPayload(ctx, effectivePayload)
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
	// Compute the dropped-filesystem set up front so it ends up on the
	// RestoreResult even when dockerRestore is never called (dry-run)
	// or short-circuits via skipDocker.
	var droppedCrewFilesystems []string
	if skipDocker {
		for _, c := range manifest.Contents.Crews {
			if c.WorkspaceIncluded || c.MemoryIncluded || c.SystemIncluded {
				droppedCrewFilesystems = append(droppedCrewFilesystems, c.Slug)
			}
		}
	}
	dockerRestore := func(_ context.Context) error {
		if skipDocker {
			// The Logger callback is best-effort (a nil Logger from an
			// API handler is allowed). slog goes through the runtime's
			// default handler and reaches the operator regardless —
			// silent data loss from a nil-Logger caller was the gap.
			if len(droppedCrewFilesystems) > 0 {
				slog.Warn("backup restore: docker phase skipped under --as-* rewrite; crew filesystem data NOT landed",
					"dropped_crews", droppedCrewFilesystems,
					"as_workspace", opts.AsWorkspace,
					"as_crew", opts.AsCrew,
				)
			}
			if opts.Logger != nil {
				opts.Logger("docker phase skipped because --as-workspace / --as-crew was supplied; provision the new crews and re-run restore without the rewrite flag to land container state")
			}
			return nil
		}
		if opts.DockerOps == nil || opts.ContainerFor == nil {
			return nil
		}
		// Preflight: every target container that ACTUALLY HAS DATA in
		// the bundle must exist before we start writing. Crews whose
		// manifest entries report WorkspaceIncluded=false AND
		// MemoryIncluded=false have no per-crew filesystem section to
		// restore — typically because they were never provisioned at
		// backup time — so requiring their containers to exist now is
		// useless friction (no data would land there anyway). Without
		// this skip, a brand-new restore target that hasn't yet
		// provisioned every crew refuses to restore the ones it CAN
		// land, even though the DB rows + the one running crew's
		// filesystem would all apply cleanly.
		for _, c := range manifest.Contents.Crews {
			if !c.WorkspaceIncluded && !c.MemoryIncluded && !c.SystemIncluded {
				continue
			}
			containerID := opts.ContainerFor(c.ID, c.Slug)
			if containerID == "" {
				continue
			}
			exists, err := opts.DockerOps.ContainerExists(ctx, containerID)
			if err != nil {
				return fmt.Errorf("backup: preflight crew %s: %w", c.Slug, err)
			}
			if !exists {
				return fmt.Errorf("backup: crew %q has filesystem data in the bundle but container %q is not provisioned on this instance; run `crewship crew provision %s` then re-run restore", c.Slug, containerID, c.Slug)
			}
		}
		for _, c := range manifest.Contents.Crews {
			if !c.WorkspaceIncluded && !c.MemoryIncluded && !c.SystemIncluded {
				// Bundle has nothing to land for this crew (DB rows
				// already restored above). Skip silently.
				continue
			}
			containerID := opts.ContainerFor(c.ID, c.Slug)
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
			Manifest:               manifest,
			RestoredWs:             firstWorkspaceSlug(extracted.DBDump),
			RestoredWorkspaceID:    firstWorkspaceID(extracted.DBDump),
			CrewsCount:             len(manifest.Contents.Crews),
			RowsInserted:           rowsSeen, // dry-run reports potential inserts
			DockerPhaseSkipped:     skipDocker,
			DroppedCrewFilesystems: droppedCrewFilesystems,
		}, nil
	}

	var stats RestoreStats
	if extracted.DBDump != nil {
		// PreInsert composition: --replace wipe FIRST (if enabled),
		// THEN user-email reconciliation. The order matters when
		// --replace drops a target user via cascade — reconciliation
		// must see the post-wipe state, not the pre-wipe state,
		// otherwise a stale "matching email" target id gets recorded
		// and the FK rewrites would point at a row that just got
		// deleted. Today users are not wiped by --replace (they're
		// global), so the ordering is defensive against future
		// schema changes.
		preInsertSteps := []func(context.Context, *sql.Tx) error{}
		if opts.Replace {
			// --replace path: wipe every workspace-scoped row that
			// belongs to the bundle's workspace by either id
			// (re-restore into same instance) or slug (post-`dev.sh
			// nuke` fresh-bootstrap workspace with the same slug but
			// a new CUID). The bundle then lands its rows with the
			// original IDs intact.
			//
			// Resolving the bundle's workspace identity from the
			// dump directly so this works even when
			// manifest.Contents.Workspace is absent (older bundles,
			// custom dumps).
			bundleID := firstWorkspaceID(extracted.DBDump)
			bundleSlug := firstWorkspaceSlug(extracted.DBDump)
			if bundleID == "" {
				return nil, fmt.Errorf("backup: --replace requires the bundle to carry a workspace row; this one does not")
			}
			preInsertSteps = append(preInsertSteps, func(ctx context.Context, tx *sql.Tx) error {
				deleted, err := ReplaceWorkspaceContents(ctx, tx, bundleID, bundleSlug)
				if err != nil {
					return err
				}
				if opts.Logger != nil && len(deleted) > 0 {
					opts.Logger(fmt.Sprintf("--replace: wiped target workspace state before restore (%d tables touched)", len(deleted)))
				}
				return nil
			})
		}
		// User reconciliation runs ALWAYS, not only under --replace.
		// The canonical "same admin email on source and target"
		// scenario produces UNIQUE(email) collisions on naive
		// INSERT OR IGNORE; bundle row drops and dependent
		// crew_members.user_id orphans → FK violation → restore
		// aborts. Aligning bundle user IDs to matching target IDs
		// (and rewriting every FK) makes the restore land cleanly.
		preInsertSteps = append(preInsertSteps, func(ctx context.Context, tx *sql.Tx) error {
			remap, err := ReconcileUsersByEmail(ctx, tx, extracted.DBDump)
			if err != nil {
				return err
			}
			if opts.Logger != nil && len(remap) > 0 {
				opts.Logger(fmt.Sprintf("user reconciliation: aligned %d bundle user(s) to target by email", len(remap)))
			}
			return nil
		})
		hooks := &RestoreDumpHooks{
			PreCommit: dockerRestore,
			PreInsert: func(ctx context.Context, tx *sql.Tx) error {
				for _, step := range preInsertSteps {
					if err := step(ctx, tx); err != nil {
						return err
					}
				}
				return nil
			},
		}
		s, err := RestoreDumpTxHooks(ctx, db, extracted.DBDump, hooks)
		if err != nil {
			return nil, err
		}
		stats = s
	} else {
		if err := dockerRestore(ctx); err != nil {
			return nil, err
		}
	}

	// Replay forward-migration backfill hooks. The bundle's manifest
	// records the migrations applied on the source instance; any
	// migration applied on the TARGET but absent from the manifest
	// represents schema that did not exist when the backup was taken.
	// Pure ADD COLUMN migrations need no special handling (DB DEFAULT
	// covers them); migrations that need to populate new columns on
	// existing rows register a RestoreBackfillFunc via migrate.go so
	// the restored rows get the same treatment.
	//
	// Runs AFTER RestoreDumpTx commits — the rows we want to backfill
	// must already be visible. A hook failure surfaces as
	// ErrRestoreBackfillFailed; the admin must investigate because the
	// main restore is already committed.
	if extracted.DBDump != nil && !opts.DryRun && len(manifest.SchemaMigrationVersions) > 0 {
		if err := replayRestoreBackfills(ctx, db, manifest.SchemaMigrationVersions, opts.Logger); err != nil {
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
		// Carry the same metadata as the success path so callers
		// inspecting RestoreResult alongside ErrNoOpRestore (audit
		// log writers, the API handler's webhook emit) see the same
		// shape and don't lose DockerPhaseSkipped / DroppedCrewFilesystems
		// / RestoredWorkspaceID just because the no-op path fired.
		return &RestoreResult{
			Manifest:               manifest,
			RestoredWs:             firstWorkspaceSlug(extracted.DBDump),
			RestoredWorkspaceID:    firstWorkspaceID(extracted.DBDump),
			CrewsCount:             len(manifest.Contents.Crews),
			RowsInserted:           0,
			DockerPhaseSkipped:     skipDocker,
			DroppedCrewFilesystems: droppedCrewFilesystems,
		}, fmt.Errorf("%w: 0 of %d rows inserted — every primary key collided with an existing row. Restore into a clean target instance, or supply --as-workspace to re-scope IDs (workspace scope only)", ErrNoOpRestore, stats.RowsSeen)
	}

	return &RestoreResult{
		Manifest:               manifest,
		RestoredWs:             firstWorkspaceSlug(extracted.DBDump),
		RestoredWorkspaceID:    firstWorkspaceID(extracted.DBDump),
		CrewsCount:             len(manifest.Contents.Crews),
		RowsInserted:           stats.RowsInserted,
		DockerPhaseSkipped:     skipDocker,
		DroppedCrewFilesystems: droppedCrewFilesystems,
	}, nil
}

// firstWorkspaceID returns the "id" of the first workspace row in
// the dump — after RemapIDs this is the freshly generated CUID, so
// callers can populate RestoreResult.RestoredWorkspaceID without a
// second lookup.
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

// rewriteWorkspaceSlug updates the single workspace row in the dump
// so --as-workspace <slug> lands under the new identity. The ID (PK)
// stays stable; only slug + display name change here.
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

// replayRestoreBackfills walks the migrations the TARGET has applied
// but the BUNDLE did not, and invokes any registered backfill hook so
// columns added post-backup get sensible values on the restored rows.
// Each hook runs in its own transaction so one failure does not strand
// a half-applied backfill. Failure returns ErrRestoreBackfillFailed
// wrapping the inner error.
//
// Hook authors: RestoreBackfillFunc REQUIRES idempotency. The retry
// path after a partial failure re-executes earlier hooks against the
// same rows; a non-idempotent hook compounds on each retry. See the
// type comment in internal/database/migrate.go for the full contract
// and idiomatic recipes.
func replayRestoreBackfills(ctx context.Context, db *sql.DB, bundleVersions []int, logger func(string)) error {
	applied := AppliedMigrationVersions(ctx, db)
	if len(applied) == 0 {
		return nil
	}
	bundleSet := make(map[int]bool, len(bundleVersions))
	for _, v := range bundleVersions {
		bundleSet[v] = true
	}
	var missing []int
	for _, v := range applied {
		if !bundleSet[v] {
			missing = append(missing, v)
		}
	}
	sort.Ints(missing)
	for _, v := range missing {
		fn := database.RestoreBackfillFor(v)
		if fn == nil {
			continue
		}
		if logger != nil {
			logger(fmt.Sprintf("restore backfill: replaying v%d", v))
		}
		// errors.Join keeps both the sentinel (so callers can use
		// errors.Is(err, ErrRestoreBackfillFailed)) AND the underlying
		// DB/tx error (so errors.As can reach the driver's concrete
		// type). A plain %w chain here could only carry one of the two
		// because fmt.Errorf supports a single wrapped error.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return errors.Join(
				ErrRestoreBackfillFailed,
				fmt.Errorf("backup: begin tx for backfill v%d: %w", v, err),
			)
		}
		if err := fn(ctx, tx, slog.Default()); err != nil {
			_ = tx.Rollback()
			return errors.Join(
				ErrRestoreBackfillFailed,
				fmt.Errorf("backup: run backfill v%d: %w", v, err),
			)
		}
		if err := tx.Commit(); err != nil {
			return errors.Join(
				ErrRestoreBackfillFailed,
				fmt.Errorf("backup: commit backfill v%d: %w", v, err),
			)
		}
	}
	return nil
}

// firstWorkspaceSlug returns the slug of the first (and typically only)
// workspace row in the dump, for populating RestoreResult.RestoredWs.
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
