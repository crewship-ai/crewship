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
	ContainerFor func(slug string) string // map crew slug -> container ID
	// DryRun, when true, runs every validation (checksum, schema-skew,
	// decrypt, payload walk) but commits no DB writes and performs no
	// docker CopyTo. RestoreResult reports what WOULD have happened.
	DryRun bool
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
}

// RestoreBackup applies a bundle to the target DB + docker engine. It
// does NOT recreate containers — the caller must provision them via
// the usual devcontainer path before calling this function, so the
// mount points exist and CopyTo has somewhere to land.
//
// In MVP this is gated to workspace / crew scope; instance scope

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
