package memory

// Memory audit watcher — closes the observability gap where agents
// inside crew containers write to /crew/agents/{slug}/.memory/
// directly via Claude Code's Write/Edit tools instead of going
// through the sidecar's POST /memory/write endpoint.
//
// Direct writes hit the host bind-mount at
// {basePath}/crews/{crewID}/agents/{slug}/.memory/{relPath}, but they
// bypass the sidecar's audit pipeline: no scrubber pass, no
// memory_versions row, no memory.updated journal entry. The HITL
// proposal flow, the retention sweeper, and any operator dashboard
// downstream all read off memory_versions — none of those features
// see a direct write today.
//
// This watcher closes the loop:
//
//   1. fsnotify watches {basePath}/crews/** for IN_CLOSE_WRITE,
//      MOVED_TO, and CREATE events on regular files (handled inside
//      memory.Watcher's existing dedup window).
//
//   2. For each changed path, parse out workspace_id, crew_id,
//      agent slug, and the tier (agent / daily / pins / learned)
//      from the path itself. Unknown shapes (not actually a memory
//      file) are skipped silently — fsnotify catches a lot of
//      noise on this tree (tmpfiles, lock files, .git, etc.).
//
//   3. Dedup check against memory_versions: if a row with this
//      exact (workspace_id, path, sha256) was inserted in the last
//      auditDedupWindow seconds, assume the sidecar already
//      recorded it and skip. Two-way protection — the sidecar's
//      write path and the watcher race; whichever lands second
//      sees the first's row and bows out.
//
//   4. Otherwise, run scrubber.Validate on the content. If hits
//      surface, emit memory.write_rejected at WARN severity — the
//      file still got written (we can't undo it from here), but
//      the operator now knows there's PII at rest. Future hardening
//      can quarantine-rename the file at this point.
//
//   5. Call RecordVersion to persist the blob + insert the
//      memory_versions row. Use WrittenBy="audit-watcher" so the
//      audit trail is honest about who recorded the version.
//
//   6. Emit memory.updated journal event so dashboards + the
//      consolidator's post-run trigger see the activity.
//
// Why retroactive instead of mounting .memory/ read-only and
// forcing sidecar IPC:
//
//   - Claude Code (and similar CLI adapters) have first-class
//     filesystem tools (Write, Edit). Telling agents "use curl
//     against localhost:9119 instead of Write" works inconsistently
//     across CLI adapters; even with explicit system-prompt
//     instructions, ~75% of write events on dev1 bypass the
//     sidecar today.
//
//   - A read-only mount with a sidecar-managed overlay is the
//     cleanest architecture but adds significant deployment
//     complexity (FUSE or layered mount) and breaks the "agents
//     see a normal filesystem" UX that the Linux container
//     framing promises.
//
//   - The retroactive watcher gives us the audit trail without
//     changing agent UX. Defense-in-depth, not gatekeeping.
//
// Wired into the server lifecycle alongside the harbormaster
// timeout sweeper. Disabled gracefully if fsnotify init fails on
// the host (Linux-only; macOS dev boxes don't have crew containers
// in the first place, so this never fires there).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"database/sql"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// auditDedupWindow is how recently a memory_versions row with the same
// (workspace, path, sha256) needs to exist for the audit watcher to
// assume the sidecar already recorded the write and skip. 60 seconds
// is generous — the sidecar's atomic-rename write usually completes
// within hundreds of milliseconds end-to-end; a longer window gives
// us safety margin against slow CI hosts without making real
// content-identical-back-to-back-writes appear undocumented.
const auditDedupWindow = 60 * time.Second

// AuditWatcherConfig parameterises StartAuditWatcher. Zero values yield
// the documented defaults; the only required field is BasePath.
type AuditWatcherConfig struct {
	// BasePath is the host-side root that contains the crews subtree.
	// Concrete path on dev1: /tmp/crewship-1-data — the watcher then
	// walks {BasePath}/crews/{crewID}/agents/{slug}/.memory/. Empty
	// disables the watcher (it logs and returns; the server boots
	// normally without it).
	BasePath string

	// BlobRoot is where RecordVersion writes the content-addressed
	// blob. Conventionally {BasePath}/versions. Empty disables blob
	// writes; the memory_versions row still lands but payload_ref
	// stays empty. Empty is the right setting on hosts where the
	// blob store is intentionally somewhere else (e.g. dedicated
	// volume).
	BlobRoot string

	// Scrubber detects PII in audited content. Required when the
	// caller wants memory.write_rejected emitted on PII findings.
	// Pass scrubber.New(ModeWarn) for the watcher-friendly mode that
	// reports findings without blocking the write (which already
	// happened on disk by the time we see it).
	Scrubber *scrubber.Scrubber

	// DebounceInterval is forwarded to memory.Watcher's WatchConfig.
	// Zero -> 1.5 s default (matches Watcher's own default).
	DebounceInterval time.Duration

	// PollFallbackInterval is forwarded to memory.Watcher. Zero ->
	// 30 s default. Polling fires when fsnotify can't keep up (Docker
	// Desktop bind-mounts, NFS, etc.).
	PollFallbackInterval time.Duration
}

// StartAuditWatcher launches a goroutine that watches BasePath/crews
// for memory-file changes and audits each one (scrubber + version row
// + journal). Returns immediately. The goroutine exits when ctx is
// cancelled OR fsnotify init fails (logged at warn; server stays up).
//
// db must be a live SQL connection; journal an Emitter; logger a
// non-nil slog.Logger. Passing nil for any of these is a programming
// error and panics — by the time we boot the audit watcher, the
// server's depencency graph has already initialised them.
func StartAuditWatcher(ctx context.Context, db *sql.DB, j journal.Emitter, cfg AuditWatcherConfig, logger *slog.Logger) {
	if logger == nil {
		panic("memory: StartAuditWatcher: nil logger")
	}
	if cfg.BasePath == "" {
		logger.Info("memory audit watcher: BasePath empty, watcher disabled")
		return
	}
	root := filepath.Join(cfg.BasePath, "crews")
	if _, err := os.Stat(root); err != nil {
		// On a fresh install the crews dir doesn't exist until the
		// first crew is provisioned. Wait for it to appear rather
		// than failing the watcher boot — the server is up; the
		// watcher just sits idle.
		logger.Info("memory audit watcher: crews root not yet present, deferring",
			"root", root, "error", err)
		// Re-attempt on a slow poll until the dir exists or ctx
		// cancels. 30 s is glacial but matches the install-time
		// expectation: a crew provision is operator-initiated, not
		// a hot-path event.
		go waitForRootThenWatch(ctx, db, j, cfg, root, logger)
		return
	}
	go runAuditWatcher(ctx, db, j, cfg, root, logger)
}

func waitForRootThenWatch(ctx context.Context, db *sql.DB, j journal.Emitter, cfg AuditWatcherConfig, root string, logger *slog.Logger) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := os.Stat(root); err == nil {
				logger.Info("memory audit watcher: crews root now present, starting watcher", "root", root)
				runAuditWatcher(ctx, db, j, cfg, root, logger)
				return
			}
		}
	}
}

func runAuditWatcher(ctx context.Context, db *sql.DB, j journal.Emitter, cfg AuditWatcherConfig, root string, logger *slog.Logger) {
	wc := WatchConfig{
		Debounce:     cfg.DebounceInterval,
		PollInterval: cfg.PollFallbackInterval,
		Logger:       logger,
	}
	w, err := StartWatcher(ctx, root, wc)
	if err != nil {
		logger.Warn("memory audit watcher: StartWatcher failed; audit disabled this boot",
			"error", err)
		return
	}
	defer w.Stop()

	logger.Info("memory audit watcher started", "root", root, "blob_root", cfg.BlobRoot)

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-w.Events():
			if !ok {
				return
			}
			for _, p := range evt.Paths {
				if err := auditOnePath(ctx, db, j, cfg, p, logger); err != nil {
					// Per-file failures are warn-level; one bad
					// path must not stop the whole watcher.
					logger.Warn("memory audit watcher: path audit failed",
						"path", p, "error", err)
				}
			}
		}
	}
}

// auditOnePath is the per-change worker: parse the path, dedup
// against memory_versions, scrub, record. Exported via the wrapper
// AuditDirectWrite for tests.
func auditOnePath(ctx context.Context, db *sql.DB, j journal.Emitter, cfg AuditWatcherConfig, fullPath string, logger *slog.Logger) error {
	parsed, ok := parseMemoryPath(cfg.BasePath, fullPath)
	if !ok {
		// Not a memory file we audit — silently skip. fsnotify
		// produces a lot of noise here (tmpfiles, hidden files,
		// container internals); spamming the log isn't useful.
		return nil
	}

	// stat-then-read so we skip directories + transient .tmp files
	// the writer creates during atomic-rename.
	info, err := os.Stat(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File deleted between fsnotify event and our stat —
			// not an error, just a stale event. Future iteration
			// can emit memory.deleted here; today we skip.
			return nil
		}
		return fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return nil
	}
	// Hidden file or staging artefact (lock files, .tmp suffix).
	base := filepath.Base(fullPath)
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".lock") {
		return nil
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// Resolve workspace_id + crew_id from the parsed crew dir.
	var workspaceID string
	if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM crews WHERE id = ?`, parsed.CrewID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Stale or out-of-band crew dir (e.g. crew was deleted
			// but the dir survived). Skip silently — there's no
			// workspace to attribute the audit to.
			return nil
		}
		return fmt.Errorf("lookup workspace for crew %s: %w", parsed.CrewID, err)
	}

	// Dedup against the sidecar's path. If a row with the same
	// (workspace, path, sha256) was inserted recently, the sidecar
	// already audited this write; we'd just duplicate the row.
	dedupCutoff := time.Now().Add(-auditDedupWindow).UTC().Format(time.RFC3339Nano)
	var existing int
	if err := db.QueryRowContext(ctx, `
		SELECT 1 FROM memory_versions
		 WHERE workspace_id = ? AND path = ? AND sha256 = ?
		   AND written_at >= ?
		 LIMIT 1`,
		workspaceID, parsed.RelPath, hash, dedupCutoff,
	).Scan(&existing); err == nil && existing == 1 {
		// Already audited within window — done.
		return nil
	}

	// Scrubber pass. ModeWarn surfaces hits but doesn't block — the
	// file already exists on disk; we can't unwrite it here. The
	// memory.write_rejected entry alerts the operator that a
	// direct-write contained PII.
	if cfg.Scrubber != nil {
		if result := cfg.Scrubber.Validate(string(content), scrubber.ModeWarn); len(result.Hits) > 0 {
			ruleNames := make([]string, 0, len(result.Hits))
			for _, h := range result.Hits {
				ruleNames = append(ruleNames, h.Pattern)
			}
			if _, jerr := j.Emit(ctx, journal.Entry{
				WorkspaceID: workspaceID,
				Type:        journal.EntryMemoryWriteRejected,
				Severity:    journal.SeverityWarn,
				ActorType:   journal.ActorSystem,
				ActorID:     "audit-watcher",
				Summary:     fmt.Sprintf("direct memory write contained PII (%d hits)", len(result.Hits)),
				Payload: map[string]any{
					"path":       parsed.RelPath,
					"crew_id":    parsed.CrewID,
					"agent_slug": parsed.AgentSlug,
					"rules":      ruleNames,
					"sha256":     hash,
				},
			}); jerr != nil {
				logger.Warn("memory audit watcher: journal emit (scrubber) failed", "error", jerr)
			}
		}
	}

	// Record the version. RecordVersion handles content-addressed
	// dedup of the blob; the row is always new. WrittenBy makes the
	// audit trail honest — operators can tell direct writes apart
	// from sidecar-mediated writes by this field.
	rec := VersionRecord{
		WorkspaceID: workspaceID,
		Path:        parsed.RelPath,
		Tier:        parsed.Tier,
		Content:     content,
		WrittenBy:   "audit-watcher",
		BlobRoot:    cfg.BlobRoot,
	}
	res, err := RecordVersion(ctx, db, rec)
	if err != nil {
		return fmt.Errorf("record version: %w", err)
	}

	// Mirror the memory.updated event the sidecar emits — same
	// shape, same consumers (post-run trigger, dashboards).
	if _, jerr := j.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryMemoryUpdated,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "audit-watcher",
		Summary:     fmt.Sprintf("memory %s updated (direct write, %d bytes)", parsed.RelPath, res.Bytes),
		Payload: map[string]any{
			"path":       parsed.RelPath,
			"tier":       string(parsed.Tier),
			"crew_id":    parsed.CrewID,
			"agent_slug": parsed.AgentSlug,
			"sha256":     res.Sha256,
			"bytes":      res.Bytes,
			"source":     "audit-watcher",
			"version_id": res.VersionID,
		},
	}); jerr != nil {
		logger.Warn("memory audit watcher: journal emit (updated) failed", "error", jerr)
	}

	return nil
}

// parsedMemoryPath decomposes a host-side memory file path into its
// semantic components. Returns ok=false when the path doesn't match
// the documented {basePath}/crews/{crewID}/agents/{slug}/.memory/ shape.
type parsedMemoryPath struct {
	CrewID    string
	AgentSlug string
	RelPath   string // shape: 'agent:{slug}/{rel}' — matches the
	// path convention RecordVersion expects so the version row's
	// path field is stable across sidecar and audit-watcher writes.
	Tier Tier
}

// parseMemoryPath does the shape match. Expected:
//
//	{basePath}/crews/{crewID}/agents/{slug}/.memory/<file-or-subdir>
//
// File mapping → Tier:
//   - AGENT.md  → TierAgent
//   - CREW.md   → TierCrew (only when path is /.memory/CREW.md at the
//     crew-shared level, but agent-level CREW.md is unusual — we map
//     anything literally named CREW.md to TierCrew)
//   - pins.md   → TierPins
//   - daily/<file>.md → TierAgent (daily logs are personal)
//   - learned-*.md → TierLearned
//   - anything else → ok=false (skip)
func parseMemoryPath(basePath, fullPath string) (parsedMemoryPath, bool) {
	rel, err := filepath.Rel(basePath, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return parsedMemoryPath{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// Expected min shape: crews/{crewID}/agents/{slug}/.memory/<file>
	if len(parts) < 6 || parts[0] != "crews" || parts[2] != "agents" || parts[4] != ".memory" {
		return parsedMemoryPath{}, false
	}
	crewID := parts[1]
	slug := parts[3]
	memoryRel := strings.Join(parts[5:], "/")
	base := filepath.Base(memoryRel)
	var tier Tier
	switch {
	case base == "AGENT.md":
		tier = TierAgent
	case base == "CREW.md":
		tier = TierCrew
	case base == "pins.md":
		tier = TierPins
	case strings.HasPrefix(base, "learned-") && strings.HasSuffix(base, ".md"):
		tier = TierLearned
	case len(parts) >= 7 && parts[5] == "daily" && strings.HasSuffix(base, ".md"):
		tier = TierAgent
	default:
		return parsedMemoryPath{}, false
	}
	// Path convention: 'agent:{slug}/{rel}' so the row is groupable
	// alongside sidecar-recorded versions of the same logical file.
	canonical := fmt.Sprintf("agent:%s/%s", slug, memoryRel)
	return parsedMemoryPath{
		CrewID:    crewID,
		AgentSlug: slug,
		RelPath:   canonical,
		Tier:      tier,
	}, true
}
