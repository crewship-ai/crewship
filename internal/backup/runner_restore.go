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
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
)

// RestoreOptions collects the parameters for RestoreBackup.
type RestoreOptions struct {
	Path        string
	Passphrase  string
	Identities  []age.Identity
	AsWorkspace string // optional slug override for workspace scope
	AsCrew      string // optional slug override for crew scope
	// FilesOnly restores ONLY the per-crew container filesystem state,
	// into the crews of the workspace named by ResumeWorkspaceID. The DB
	// dump is not applied.
	//
	// It exists because disaster recovery genuinely takes three steps and
	// only ever had two that worked (#1716). A --as-workspace / --as-crew
	// restore forks the bundle's rows under a new identity but cannot
	// land container state — the crews it just created have no containers
	// yet. The operator provisions them, and then needs a way to say
	// "now land the files into what I just provisioned". Re-running the
	// full restore cannot do that: it would re-insert the bundle's rows
	// under their ORIGINAL ids, into a workspace that already holds
	// remapped copies of all of them.
	//
	// Authorised by provenance, not by a flag: the resume is permitted
	// only for a workspace that backup_restore_origins records as having
	// been forked from THIS bundle (matched on payload digest). See
	// origins.go.
	FilesOnly bool
	// ResumeWorkspaceID is the caller's current workspace, used to look
	// up the provenance that authorises a FilesOnly restore and to
	// resolve the bundle's crew slugs onto this instance's crew rows.
	ResumeWorkspaceID string
	Actor             Actor
	DockerOps         DockerOps
	ContainerFor      func(id, slug string) string // map crew (id, slug) -> container ID
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
	// BlobRoot is the content-addressed memory-version blob directory
	// on THIS (target) instance — {MemoryRoot}/versions. When set and
	// the bundle carries a memory-blobs section, RestoreBackup writes
	// each blob under blobRoot (idempotent — existing content-addressed
	// files are left alone) AND rewrites every restored memory_versions
	// row's payload_ref to point at blobRoot instead of whatever
	// absolute path the SOURCE instance recorded. That rewrite matters
	// even when restoring onto "the same host": payload_ref is an
	// absolute filesystem path baked in at write time
	// (internal/memory/versions.go RecordVersion), so leaving it
	// untouched would restore rows pointing at a path this instance
	// never had. Empty skips both steps — matches CreateOptions.BlobRoot's
	// "empty disables" convention.
	BlobRoot string
}

// RestoreResult summarises what was restored.
type RestoreResult struct {
	Manifest            *Manifest
	RestoredWs          string
	RestoredWorkspaceID string // new CUID when --as-workspace remapped IDs
	CrewsCount          int
	// CrewsRestored counts crews whose CONTAINER STATE actually landed,
	// which is not the same number as CrewsCount — that one reports what
	// the bundle describes. A restore can walk every crew and write to
	// none of them (no container resolved, no provenance covering it),
	// and reporting the bundle's count for that is how a no-op reads as
	// a success.
	CrewsRestored      int
	RowsInserted       int
	DockerPhaseSkipped bool
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
	// SecurityLevelClamped counts credentials rows whose bundle-supplied
	// security_level was not a tier keeper's table defines, and which
	// were therefore written at the strictest tier instead (#1603). On a
	// dry run this is what WOULD be clamped.
	//
	// Structured for the same reason DroppedCrewFilesystems is: the
	// admin who has to go and re-tier those credentials cannot do it
	// from a log line the API handler never printed.
	SecurityLevelClamped int
	// SecurityLevelClamps is a bounded sample of the above, with the
	// credential id, its name, and the value the bundle carried.
	SecurityLevelClamps []SecurityLevelClamp
}

// warnSecurityLevelClamps emits the operator-facing warning for a restore
// that had to rewrite credential tiers. Shared by the dry-run and the
// committed path so the two cannot describe the same bundle differently.
func warnSecurityLevelClamps(logger func(string), clamps []SecurityLevelClamp, total int, dryRun bool) {
	if total == 0 || logger == nil {
		return
	}
	verb := "clamped to"
	if dryRun {
		verb = "would be clamped to"
	}
	strictest := strictestSecurityLevel()
	details := make([]string, 0, len(clamps))
	for _, c := range clamps {
		label := c.CredentialID
		if c.Name != "" {
			label = fmt.Sprintf("%s (%s)", c.Name, c.CredentialID)
		}
		details = append(details, fmt.Sprintf("%s: %s", label, c.From))
	}
	more := ""
	if total > len(clamps) {
		more = fmt.Sprintf(" (+%d more)", total-len(clamps))
	}
	logger(fmt.Sprintf(
		"WARNING: %d restored credential(s) carried a security_level outside the tier table and %s %s, the strictest tier — re-set each one deliberately with `crewship credential update <name> --security-level N`: %s%s",
		total, verb, strictest.Label(), strings.Join(details, "; "), more))
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

	// Disaster-recovery provenance (#1716). Two directions:
	//
	//   - A FilesOnly resume READS it, to learn which local crews the
	//     bundle's crews became. Without a row that matches this exact
	//     bundle the resume is refused — the flag alone authorises
	//     nothing.
	//   - A rewritten restore (--as-workspace / --as-crew) WRITES it,
	//     inside the same transaction as the rows it describes, so a
	//     later resume has something to be authorised by.
	// The provenance row is looked up on EVERY restore that names a
	// caller workspace, not only on a --files-only one, because it
	// answers two different questions and the second is the dangerous
	// one:
	//
	//   FilesOnly     — "which local crews did this bundle's crews
	//                    become?" Without a matching row the resume is
	//                    refused; the flag alone authorises nothing.
	//   NOT FilesOnly — "is the caller standing in a workspace that was
	//                    FORKED from this bundle?" If so, an ordinary
	//                    restore is the wrong operation and must not
	//                    run: the crews it would address come from the
	//                    manifest, which still carries the SOURCE crew's
	//                    identity, so on a same-instance restore it
	//                    overwrites the live crew's workspace and memory
	//                    with the older backup while INSERT OR IGNORE
	//                    makes the row counts look untroubled.
	//
	// That second case is exactly the command the CLI used to print
	// ("re-run restore without the rewrite flag"), so it is the one an
	// operator is most likely to type from memory or from an old
	// runbook. It gets a refusal that names the mode that is safe.
	var origin *RestoreOrigin
	if opts.ResumeWorkspaceID != "" {
		o, oerr := LookupRestoreOrigin(ctx, db, opts.ResumeWorkspaceID)
		if oerr != nil {
			return nil, oerr
		}
		origin = o
	}
	forkedFromThisBundle := origin != nil &&
		manifest.Checksums.PayloadSHA256 != "" &&
		origin.BundleSHA256 == manifest.Checksums.PayloadSHA256

	if !opts.FilesOnly && forkedFromThisBundle && opts.AsWorkspace == "" && opts.AsCrew == "" {
		return nil, fmt.Errorf("backup: workspace %s was created by restoring this bundle, so an ordinary restore here would address the crews the BUNDLE names — on this instance those are the source crews, whose workspace and agent memory it would overwrite with this older backup. "+
			"Its rows are already present under new ids. To land container state into the crews this workspace actually has, use --files-only",
			opts.ResumeWorkspaceID)
	}

	if opts.FilesOnly {
		if opts.ResumeWorkspaceID == "" {
			return nil, fmt.Errorf("backup: --files-only requires a target workspace")
		}
		if opts.AsWorkspace != "" || opts.AsCrew != "" || opts.Replace {
			return nil, fmt.Errorf("backup: --files-only cannot be combined with --as-workspace, --as-crew or --replace; it lands container state into crews that already exist")
		}
		if !forkedFromThisBundle {
			return nil, fmt.Errorf("backup: workspace %s was not created by restoring this bundle, so there is no crew mapping to land files through; run the rewrite restore first (`--as-workspace`/`--as-crew`), provision the crews, then re-run with --files-only", opts.ResumeWorkspaceID)
		}
	}

	// Stage DB rewrites before any writes so both --as-* flags and the
	// FK rows land consistently. A FilesOnly resume applies no DB dump
	// at all, so none of this runs for it.
	// bundleCrewSlugs snapshots each crew row's ORIGINAL slug, by row
	// position, before rewriteCrewSlug and RemapIDs mutate the dump in
	// place. Pairing by position afterwards yields the bundle-slug ->
	// created-crew map the DR resume needs; taking it after the rewrites
	// would only ever recover the new identity, which is the half we
	// already know.
	var bundleCrewSlugs []string
	if extracted.DBDump != nil && !opts.FilesOnly {
		for _, r := range extracted.DBDump.Tables["crews"] {
			slug, _ := r["slug"].(string)
			bundleCrewSlugs = append(bundleCrewSlugs, slug)
		}
		if opts.AsWorkspace != "" {
			rewriteWorkspaceSlug(extracted.DBDump, opts.AsWorkspace)
		}
		if opts.AsCrew != "" && manifest.Scope == ScopeCrew && len(manifest.Contents.Crews) > 0 {
			rewriteCrewSlug(extracted.DBDump, manifest.Contents.Crews[0].ID, opts.AsCrew)
			// A crew-scope bundle carries its parent `workspaces` row
			// purely to satisfy FK columns (crews.workspace_id,
			// chats.workspace_id, agents.workspace_id, …) — DumpCrew
			// pulls in exactly the one row matching the crew's
			// workspace_id. --as-crew forks the CREW under a fresh
			// slug, but until this rewrite the carried workspace row
			// kept its ORIGINAL slug. RemapIDs (below) regenerates its
			// id unconditionally, and workspaces.slug is globally
			// UNIQUE: on a same-instance restore (the documented,
			// common case — "crew-scope bundles restore independently
			// of their parent workspace") the target already has a
			// workspace row under that same slug, so the freshly
			// regenerated row collided on INSERT OR IGNORE and was
			// silently dropped, stranding every workspace_id FK the
			// crew's rows point at (#1190).
			//
			// Forking the workspace's slug alongside the crew's — not
			// just its id — fixes that AND sidesteps a second-order
			// collision: agents has UNIQUE(workspace_id, slug), so a
			// forked crew whose agents kept the ORIGINAL (shared)
			// workspace_id would still collide against the source
			// crew's agents of the same slug. Giving the fork its own
			// dedicated workspace avoids every UNIQUE(workspace_id, …)
			// collision at once, matching how --as-workspace already
			// behaves for workspace-scope bundles.
			rewriteWorkspaceSlug(extracted.DBDump, opts.AsCrew)
		}
		// When the admin picked a new slug via --as-* they want the
		// restored data to live alongside the source. Regenerate every
		// primary key and rewrite every FK so INSERT OR IGNORE does
		// not drop the whole bundle on PK collision.
		if opts.AsWorkspace != "" || opts.AsCrew != "" {
			if err := RemapIDs(ctx, db, extracted.DBDump); err != nil {
				return nil, err
			}
			ensureRestoringUserMembership(ctx, db, extracted.DBDump, opts.Actor.UserID, opts.Actor.Role)
		}
		// memory_versions.payload_ref is an absolute filesystem path
		// baked in on the SOURCE instance at RecordVersion time. Unlike
		// the --as-* rewrites above, this one runs on EVERY restore
		// (not just forked-slug ones) — the path problem exists even
		// for a same-slug disaster-recovery restore onto a fresh
		// instance, because {MemoryRoot}/versions is rarely the same
		// absolute path on two installs. Content-addressing makes the
		// rewrite safe: the new payload_ref is always recomputed from
		// the row's own sha256, never trusted from the bundle as-is.
		rewriteMemoryVersionsPayloadRef(extracted.DBDump, opts.BlobRoot)
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

	// crewTargetFor maps a manifest crew entry onto the container this
	// restore should write into.
	//
	// On a normal restore that is the crew's own recorded identity. On a
	// FilesOnly resume it is NOT: the bundle's crews were re-created
	// under fresh ids (and, under --as-crew, a fresh slug), so resolving
	// from the manifest would address the SOURCE crew's container — on a
	// same-instance DR that is the source crew's live data being
	// overwritten by a sibling crew's backup. The provenance map
	// recorded at fork time is the only thing that relates the two.
	crewTargetFor := func(c CrewSummary) (id, slug string, ok bool) {
		if !opts.FilesOnly {
			return c.ID, c.Slug, true
		}
		if origin == nil {
			return "", "", false
		}
		target, found := origin.CrewsByBundleSlug[c.Slug]
		if !found || target.ID == "" {
			return "", "", false
		}
		return target.ID, target.Slug, true
	}
	// Compute the dropped-filesystem set up front so it ends up on the
	// RestoreResult even when dockerRestore is never called (dry-run)
	// or short-circuits via skipDocker.
	var droppedCrewFilesystems []string
	if skipDocker {
		for _, c := range manifest.Contents.Crews {
			if c.HasFilesystemSections(manifest.FormatVersion) {
				droppedCrewFilesystems = append(droppedCrewFilesystems, c.Slug)
			}
		}
	}
	// A bundle written before #1713 was fixed carries no crew memory
	// tree, and reports memory_included: true anyway. Restoring one is
	// legitimate — its workspace files, volumes and DB rows are all
	// real — but the operator has to hear, once, that the memory is not
	// coming back, because the bundle itself will keep claiming
	// otherwise for as long as it exists.
	if manifest.FormatVersion < FormatVersionCrewMemory {
		var claimed []string
		for _, c := range manifest.Contents.Crews {
			if c.MemoryIncluded {
				claimed = append(claimed, c.Slug)
			}
		}
		if len(claimed) > 0 {
			msg := fmt.Sprintf("bundle format v%d predates the crew-memory fix (#1713): it carries NO agent or crew-shared memory for %s, "+
				"despite its manifest reporting memory_included. Everything else in the bundle restores normally; "+
				"agent memory from before this bundle was taken cannot be recovered from it",
				manifest.FormatVersion, strings.Join(claimed, ", "))
			slog.Warn("backup restore: " + msg)
			if opts.Logger != nil {
				opts.Logger(msg)
			}
		}
	}
	// crewsRestored counts crews whose container state ACTUALLY landed,
	// as distinct from the crews the bundle happens to describe. The
	// difference is the whole of #1713 restated one layer up: a
	// `--files-only` resume can walk its loop and write nothing —
	// ContainerFor returning "" for every crew, or a provenance map that
	// covers none of them — and, without this, would report the
	// manifest's crew count and exit 0. An operator would read that as
	// "my crews' files are back".
	crewsRestored := 0
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
				opts.Logger(restoreForkedWorkspaceRemediation())
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
			if !c.HasFilesystemSections(manifest.FormatVersion) {
				continue
			}
			targetID, targetSlug, ok := crewTargetFor(c)
			if !ok {
				return fmt.Errorf("backup: --files-only: bundle crew %q has no crew on this instance recorded against it; the workspace's restore provenance does not cover it", c.Slug)
			}
			containerID := opts.ContainerFor(targetID, targetSlug)
			if containerID == "" {
				continue
			}
			exists, err := opts.DockerOps.ContainerExists(ctx, containerID)
			if err != nil {
				return fmt.Errorf("backup: preflight crew %s: %w", targetSlug, err)
			}
			if !exists {
				return fmt.Errorf("%s", restoreMissingContainerRemediation(targetSlug, containerID))
			}
		}
		for _, c := range manifest.Contents.Crews {
			if !c.HasFilesystemSections(manifest.FormatVersion) {
				// Bundle has nothing to land for this crew (DB rows
				// already restored above). Skip silently.
				continue
			}
			targetID, targetSlug, ok := crewTargetFor(c)
			if !ok {
				continue
			}
			containerID := opts.ContainerFor(targetID, targetSlug)
			if containerID == "" {
				continue
			}
			// c.Slug (the BUNDLE's slug) selects the section inside the
			// payload; targetSlug names the crew being written to. On a
			// --as-crew resume the two differ, and swapping them silently
			// restores nothing.
			if err := RestoreCrew(ctx, opts.DockerOps, containerID, c.Slug, extracted); err != nil {
				// Degraded memory permissions are not a failed restore:
				// every section landed, and re-running cannot help
				// because the entry that blocked the chgrp blocks it
				// again. Failing here would roll back a DB transaction
				// over data already written to the container — the
				// worst of both. Surface it where the operator will see
				// it and carry on counting this crew as restored.
				if errors.Is(err, ErrMemoryPermsDegraded) {
					slog.Warn("backup restore: crew data landed but its memory permissions are degraded",
						"crew", targetSlug, "error", err)
					if opts.Logger != nil {
						opts.Logger(fmt.Sprintf("crew %s restored, but its memory tree permissions could not be fully re-applied — the memory sidecar may not be able to write it: %v", targetSlug, err))
					}
				} else {
					return fmt.Errorf("backup: restore crew %s: %w", targetSlug, err)
				}
			}
			crewsRestored++
			if opts.FilesOnly && opts.Logger != nil {
				opts.Logger(fmt.Sprintf("landed container state for crew %s (from bundle crew %s)", targetSlug, c.Slug))
			}
		}
		return nil
	}
	// memoryBlobsRestore lands the memory-blobs/ section (if any) onto
	// opts.BlobRoot. Unlike dockerRestore this is NOT gated on
	// skipDocker — memory_versions blobs are workspace-scoped content-
	// addressed files, not per-crew container state, so --as-workspace
	// / --as-crew forking a new workspace/crew identity doesn't change
	// which blobs are needed (the sha references are unchanged; only
	// rewriteMemoryVersionsPayloadRef above needed to run regardless of
	// scope too). Runs inside PreCommit alongside dockerRestore so a
	// write failure here also rolls back the DB insert, matching the
	// "docker phase failure leaves no half-restored rows" guarantee.
	memoryBlobsRestore := func(ctx context.Context) error {
		// expectedShas is sourced from the DB dump this restore is
		// landing — NOT from the archive being walked inside
		// RestoreMemoryBlobs. See memoryVersionShaSet's doc comment: this
		// is what lets the write destination for every blob be derived
		// from a trusted value instead of an arbitrary tar entry name.
		expectedShas := memoryVersionShaSet(extracted.DBDump)
		n, err := RestoreMemoryBlobs(ctx, opts.BlobRoot, extracted, expectedShas)
		if err != nil {
			return fmt.Errorf("backup: restore memory blobs: %w", err)
		}
		if n > 0 && opts.Logger != nil {
			opts.Logger(fmt.Sprintf("restored %d memory-version blob(s)", n))
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
		// A dry run's whole point is telling the admin what a real
		// restore would do. "This bundle carries credentials at a tier
		// that does not exist" is the most actionable thing it can say,
		// so it is reported here as well as on the committed path.
		clamps, clamped := InspectSecurityLevels(extracted.DBDump)
		warnSecurityLevelClamps(opts.Logger, clamps, clamped, true)
		return &RestoreResult{
			Manifest:               manifest,
			RestoredWs:             firstWorkspaceSlug(extracted.DBDump),
			RestoredWorkspaceID:    firstWorkspaceID(extracted.DBDump),
			CrewsCount:             len(manifest.Contents.Crews),
			RowsInserted:           rowsSeen, // dry-run reports potential inserts
			DockerPhaseSkipped:     skipDocker,
			DroppedCrewFilesystems: droppedCrewFilesystems,
			SecurityLevelClamped:   clamped,
			SecurityLevelClamps:    clamps,
		}, nil
	}

	// A FilesOnly resume applies no DB dump: the rows are already on this
	// instance, under the ids the fork gave them. All that is left is the
	// docker phase, addressed through the provenance map, plus the
	// content-addressed memory blobs (idempotent, and keyed by sha rather
	// than by any identity the fork changed).
	if opts.FilesOnly {
		// Landing container state is the ONLY thing this mode does, so a
		// caller that cannot reach docker is asking for a no-op, not for
		// a restore. Refuse rather than return success having written
		// nothing.
		if opts.DockerOps == nil || opts.ContainerFor == nil {
			return nil, fmt.Errorf("backup: --files-only needs a container runtime; this instance has none configured, so there is nothing it could land")
		}
		if err := memoryBlobsRestore(ctx); err != nil {
			return nil, err
		}
		if err := dockerRestore(ctx); err != nil {
			return nil, err
		}
		if crewsRestored == 0 {
			return nil, fmt.Errorf("%s",
				restoreFilesOnlyEmptyRemediation(len(manifest.Contents.Crews), opts.ResumeWorkspaceID))
		}
		// RestoredWs is a SLUG everywhere else — it is what the CLI
		// prints as `workspace=`. Resolve it rather than echoing the id
		// into a field the operator reads as a name; a best-effort
		// lookup because a failure here must not fail a restore that has
		// already landed.
		restoredSlug := opts.ResumeWorkspaceID
		var slug string
		if err := db.QueryRowContext(ctx,
			`SELECT slug FROM workspaces WHERE id = ?`, opts.ResumeWorkspaceID,
		).Scan(&slug); err == nil && slug != "" {
			restoredSlug = slug
		}
		return &RestoreResult{
			Manifest:            manifest,
			RestoredWs:          restoredSlug,
			RestoredWorkspaceID: opts.ResumeWorkspaceID,
			CrewsCount:          len(manifest.Contents.Crews),
			CrewsRestored:       crewsRestored,
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
			// Provenance for the DR resume (#1716), written only for a
			// rewritten restore — that is the only case that creates a
			// workspace whose identity the tenant guard cannot recognise
			// later. Inside the tx, so a rolled back restore leaves no
			// claim behind.
			PostInsert: func(ctx context.Context, tx *sql.Tx) error {
				if !skipDocker {
					return nil
				}
				return recordForkOrigin(ctx, tx, opts, manifest, extracted.DBDump, bundleCrewSlugs)
			},
			PreCommit: func(ctx context.Context) error {
				if err := memoryBlobsRestore(ctx); err != nil {
					return err
				}
				return dockerRestore(ctx)
			},
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
		warnSecurityLevelClamps(opts.Logger, stats.SecurityLevelClamps, stats.SecurityLevelClamped, false)
	} else {
		if err := memoryBlobsRestore(ctx); err != nil {
			return nil, err
		}
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
			SecurityLevelClamped:   stats.SecurityLevelClamped,
			SecurityLevelClamps:    stats.SecurityLevelClamps,
		}, fmt.Errorf("%w: 0 of %d rows inserted — every primary key collided with an existing row. Restore into a clean target instance, or supply --as-workspace to re-scope IDs (workspace scope only)", ErrNoOpRestore, stats.RowsSeen)
	}

	return &RestoreResult{
		Manifest:            manifest,
		RestoredWs:          firstWorkspaceSlug(extracted.DBDump),
		RestoredWorkspaceID: firstWorkspaceID(extracted.DBDump),
		CrewsCount:          len(manifest.Contents.Crews),
		// Set on the ordinary path too, not just the resume: reporting 0
		// here while crews_count says 4 would be a fresh way of lying
		// about what landed, in a field added to stop exactly that.
		CrewsRestored:          crewsRestored,
		RowsInserted:           stats.RowsInserted,
		DockerPhaseSkipped:     skipDocker,
		DroppedCrewFilesystems: droppedCrewFilesystems,
		SecurityLevelClamped:   stats.SecurityLevelClamped,
		SecurityLevelClamps:    stats.SecurityLevelClamps,
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

// ensureRestoringUserMembership grants the admin who ran the restore
// membership on the newly-forked workspace. --as-workspace / --as-crew
// fork a BRAND NEW workspace (rewriteWorkspaceSlug + RemapIDs above)
// that the restoring admin otherwise has no way to reach afterward:
// crew-scope bundles (DumpCrew) never carry workspace_members rows at
// all, and even workspace-scope bundles only carry the SOURCE
// workspace's membership list, which may not include this admin.
// Without this, `crewship workspace list` doesn't show the fork and
// addressing it directly by ID/slug 403s (#1215).
//
// Uses opts.Actor.Role — RestoreBackup already required it to be
// OWNER or ADMIN via RequireAdmin, matching what `workspace create`
// grants its own caller.
//
// Only inserts when a `users` row for userID is actually reachable
// (carried in the bundle itself, or already present on the target
// instance — the normal case, since the restoring admin authenticated
// against this instance to run the restore in the first place). Skips
// silently otherwise rather than adding a row FK-checks would reject.
func ensureRestoringUserMembership(ctx context.Context, db *sql.DB, dump *DBDump, userID, role string) {
	if dump == nil || userID == "" {
		return
	}
	wsID := firstWorkspaceID(dump)
	if wsID == "" {
		return
	}
	for _, r := range dump.Tables["workspace_members"] {
		if r["workspace_id"] == wsID && r["user_id"] == userID {
			return // already a member (e.g. carried over + reconciled by email)
		}
	}
	if !userRowReachable(ctx, db, dump, userID) {
		return
	}
	if role == "" {
		role = "OWNER"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	dump.Tables["workspace_members"] = append(dump.Tables["workspace_members"], map[string]any{
		"id":           newRemapCUID(),
		"workspace_id": wsID,
		"user_id":      userID,
		"role":         role,
		"created_at":   now,
		"updated_at":   now,
	})
}

// userRowReachable reports whether userID will resolve against
// `users` once the restore's INSERTs land — either because the
// bundle carries that row itself, or because the target DB already
// has it (the restoring admin is, by definition, an authenticated
// user on this instance).
func userRowReachable(ctx context.Context, db *sql.DB, dump *DBDump, userID string) bool {
	for _, r := range dump.Tables["users"] {
		if r["id"] == userID {
			return true
		}
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)`, userID).Scan(&exists); err != nil {
		return false
	}
	return exists
}

// rewriteCrewSlug does the equivalent for crew-scope restores.
// recordForkOrigin writes the provenance row for a rewritten restore:
// which bundle this workspace was forked from, and which local crew each
// of the bundle's crews became.
//
// bundleCrewSlugs is the crews table's ORIGINAL slugs by row position,
// snapshotted before the rewrites; the dump's crew rows now carry the
// post-remap identities at the same positions. Pairing them is what lets
// a later --files-only resume address the crew this restore created
// rather than the one the bundle names — which, on a same-instance
// restore, is the source crew whose live data would be overwritten.
//
// A length mismatch means something reordered or resized the crews table
// between the snapshot and here; rather than guess at the pairing, record
// no map. The resume then refuses with a clear message instead of
// writing a crew's files into a different crew.
func recordForkOrigin(ctx context.Context, tx *sql.Tx, opts RestoreOptions, manifest *Manifest, dump *DBDump, bundleCrewSlugs []string) error {
	if dump == nil {
		return nil
	}
	workspaceID := firstWorkspaceID(dump)
	if workspaceID == "" {
		return nil
	}
	crews := map[string]RestoreOriginCrew{}
	rows := dump.Tables["crews"]
	if len(rows) == len(bundleCrewSlugs) {
		for i, r := range rows {
			bundleSlug := bundleCrewSlugs[i]
			if bundleSlug == "" {
				continue
			}
			id, _ := r["id"].(string)
			slug, _ := r["slug"].(string)
			if id == "" {
				continue
			}
			crews[bundleSlug] = RestoreOriginCrew{ID: id, Slug: slug}
		}
	} else {
		slog.Warn("backup restore: crew row count changed during rewrite; recording origin without a crew map",
			"snapshot", len(bundleCrewSlugs), "after", len(rows), "workspace_id", workspaceID)
	}
	return RecordRestoreOrigin(ctx, tx, RestoreOrigin{
		WorkspaceID:       workspaceID,
		BundleSHA256:      manifest.Checksums.PayloadSHA256,
		BundlePath:        opts.Path,
		CrewsByBundleSlug: crews,
		RestoredAt:        time.Now().UTC(),
		RestoredBy:        opts.Actor.UserID,
	})
}

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
