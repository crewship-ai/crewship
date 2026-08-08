package server

// #1845 — the scheduled half. Detection of a stale crew image now exists
// (internal/provider/docker/crew_image_freshness.go) but is only ever
// consulted when someone asks, and the operator this issue is about is
// precisely the one who is not asking: their crew containers have been up for
// weeks and nothing has prompted them to look.
//
// This sweep is the prompt. It runs on the scheduler that already exists,
// reads every live crew once a day, and journals the ones that are behind —
// from which the notify bridge (journal.EntryImageStale → system.health) does
// the rest.
//
// # Why a daily cron and not a per-run check
//
// The condition is not event-shaped. A crew is behind from the moment the tag
// moves until someone recycles it, which is days; nothing about a particular
// agent run makes it more or less true. Checking per run would issue a
// registry HEAD on the hot path of every dispatch and would have to be
// suppressed anyway. Once a day, off-peak, is the cadence the fact deserves.
//
// # Why the de-duplication lives in the journal
//
// Because the sweep must remember across restarts, and the in-memory map the
// sidecar signal uses (Orchestrator.staleSidecarJournaled) cannot. That map is
// right for its own producer — stale-sidecar detection fires on every RunAgent,
// so what it needs is within-process suppression at high frequency, and a
// restart re-alerting once a fortnight is invisible against that. Here the
// producer fires once a day, so an in-memory set would give a redeploy — which
// on an actively developed instance is weekly — the power to re-alert on a
// condition nobody has touched. The journal is already durable, already
// queried, and already the thing the notification was projected from.
//
// The key is (crew, running digest, resolved digest), mirroring the sidecar
// signal's (container, hash) discipline: the same pair stays quiet forever,
// while a crew that was recycled and has since fallen behind again is a NEW
// pair and legitimately re-alerts.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scheduler"
)

// cronImageFreshness: daily at 04:00 UTC. Deliberately AFTER the two Keeper
// Phase 2 sweeps (03:00 and 03:30) rather than alongside them — this one makes
// a registry round-trip per distinct image and there is no reason for it to
// compete with their LLM budget for the same minute.
const cronImageFreshness = "0 4 * * *"

// imageFreshnessRoutineName is what the scheduler logs on every fire.
const imageFreshnessRoutineName = "image_freshness"

// registerImageFreshnessRoutine wires the daily sweep, and reports whether it
// took.
//
// Skipped with an info log — never an error — when the container provider does
// not implement provider.CrewImageFreshness. That is not a failure: the
// apple-container provider has no registry-digest story, and a boot that hard-
// failed over an optional observability sweep would be a worse outcome than
// the missing sweep.
func registerImageFreshnessRoutine(
	sched *scheduler.Scheduler,
	db *sql.DB,
	container provider.ContainerProvider,
	emitter journal.Emitter,
	logger *slog.Logger,
) bool {
	if sched == nil || db == nil {
		logger.Info("image freshness: sweep NOT registered (scheduler or DB unavailable)")
		return false
	}
	fresh, ok := container.(provider.CrewImageFreshness)
	if !ok {
		logger.Info("image freshness: sweep NOT registered (container provider cannot report image freshness)")
		return false
	}
	if emitter == nil {
		// Without a journal there is nothing to notify FROM: the bridge is a
		// journal commit observer. Logging the finding would put it back on
		// the channel #1845 exists to stop relying on.
		logger.Info("image freshness: sweep NOT registered (no journal wired)")
		return false
	}

	fn := func(ctx context.Context) {
		runImageFreshnessSweep(ctx, db, fresh, emitter, logger)
	}
	if err := sched.RegisterPlatformRoutine(imageFreshnessRoutineName, cronImageFreshness, fn); err != nil {
		logger.Error("image freshness: sweep registration failed", "error", err)
		return false
	}
	return true
}

// imageFreshnessRemediation is the command the notification tells an operator
// to run. Kept next to the emitter so the journal payload, the CLI's own help
// and the docs cannot drift into naming three different fixes.
const imageFreshnessRemediation = "crewship crew refresh-image"

// runImageFreshnessSweep walks every live crew, asks the provider whether its
// container is behind, and journals the ones that are.
//
// Best-effort per crew, matching the Keeper sweeps: one crew's unreachable
// daemon must not cost the rest of the fleet its check.
func runImageFreshnessSweep(
	ctx context.Context,
	db *sql.DB,
	fresh provider.CrewImageFreshness,
	emitter journal.Emitter,
	logger *slog.Logger,
) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, workspace_id, slug, COALESCE(name, ''),
		       COALESCE(runtime_image, ''), COALESCE(cached_image, '')
		  FROM crews
		 WHERE deleted_at IS NULL
		 ORDER BY workspace_id, id ASC`)
	if err != nil {
		logger.Error("image freshness: query crews failed", "error", err)
		return
	}

	type crewRow struct {
		id, workspaceID, slug, name, image, cachedImage string
	}
	var crews []crewRow
	for rows.Next() {
		var c crewRow
		if err := rows.Scan(&c.id, &c.workspaceID, &c.slug, &c.name, &c.image, &c.cachedImage); err != nil {
			logger.Warn("image freshness: scan crew row", "error", err)
			continue
		}
		crews = append(crews, c)
	}
	if err := rows.Err(); err != nil {
		logger.Error("image freshness: iterate crews failed", "error", err)
	}
	// Closed before the per-crew work below: each iteration makes a registry
	// round-trip, and holding a cursor open across the whole fleet's worth of
	// those is how a sweep ends up holding a SQLite read lock for minutes.
	_ = rows.Close()

	var scanned, behind, alerted int
	for _, c := range crews {
		if ctx.Err() != nil {
			return
		}
		scanned++
		st, err := fresh.CrewImageState(ctx, provider.CrewConfig{
			ID:          c.id,
			Slug:        c.slug,
			Image:       c.image,
			CachedImage: c.cachedImage,
		})
		if err != nil {
			logger.Warn("image freshness: state lookup failed", "crew_id", c.id, "error", err)
			continue
		}
		if !st.Behind {
			continue
		}
		behind++
		already, err := imageStaleAlreadyJournalled(ctx, db, c.id, st.RunningDigest, st.ResolvedDigest)
		if err != nil {
			// Fail CLOSED on the dedup lookup: re-emitting on a DB hiccup is
			// the noise this sweep exists to avoid, and the next daily run
			// re-checks anyway. A missed notification about a condition that
			// persists for days costs a day; a duplicate costs the category.
			logger.Warn("image freshness: dedup lookup failed, skipping emit",
				"crew_id", c.id, "error", err)
			continue
		}
		if already {
			continue
		}
		emitImageStale(ctx, emitter, logger, c.workspaceID, c.id, displayName(c.name, c.slug), st)
		alerted++
	}
	logger.Info("image freshness: daily sweep complete",
		"scanned", scanned, "behind", behind, "alerted", alerted)
}

// displayName prefers the crew's human name and falls back to its slug — a
// notification that says "crew_c8f2…" has spent its one line on something the
// reader cannot act on.
func displayName(name, slug string) string {
	if name != "" {
		return name
	}
	return slug
}

// imageStaleAlreadyJournalled reports whether this exact (crew, running,
// resolved) observation has already been recorded.
//
// Reads the journal directly rather than keeping its own table: the row it is
// looking for is the one the notification was projected from, so a separate
// marker could disagree with it — and the failure mode of disagreeing is
// either a duplicate alert or a silently swallowed one.
func imageStaleAlreadyJournalled(ctx context.Context, db *sql.DB, crewID, runningDigest, resolvedDigest string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `
		SELECT 1
		  FROM journal_entries
		 WHERE crew_id = ?
		   AND entry_type = ?
		   AND json_extract(payload, '$.running_digest') = ?
		   AND json_extract(payload, '$.resolved_digest') = ?
		 LIMIT 1`,
		crewID, string(journal.EntryImageStale), runningDigest, resolvedDigest).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// emitImageStale writes the durable record the notify bridge routes.
//
// Severity is WARN, not error, and that is the second half of the "is this one
// notification or two?" decision. The sidecar signal is severity:error because
// something is malfunctioning right now. An image that is merely behind is not
// malfunctioning — and severityPriority maps warn→medium, error→high, so
// emitting this at error would push a hygiene notice past the min_priority
// floors operators set precisely to keep hygiene notices out.
func emitImageStale(
	ctx context.Context,
	emitter journal.Emitter,
	logger *slog.Logger,
	workspaceID, crewID, crewName string,
	st *provider.CrewImageState,
) {
	summary := fmt.Sprintf(
		"%s is running an older build of %s — the tag has moved on since its container was created. Run `%s %s` to pick up the current image.",
		crewName, st.Image, imageFreshnessRemediation, crewName)

	if _, err := emitter.Emit(ctx, journal.Entry{
		// Both scopes are required, not decorative: the notify bridge drops
		// any entry with an empty WorkspaceID (there is no audience to fan out
		// to), so an unscoped emit would be journalled and never delivered —
		// which is the exact state #1845 was filed about.
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryImageStale,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     imageFreshnessRoutineName,
		Summary:     summary,
		Payload: map[string]any{
			"image":           st.Image,
			"running_digest":  st.RunningDigest,
			"resolved_digest": st.ResolvedDigest,
			"crew_name":       crewName,
			"remediation":     imageFreshnessRemediation,
		},
		Refs: map[string]any{"crew_id": crewID, "container_id": st.ContainerID},
	}); err != nil {
		logger.Warn("image freshness: journal emit failed", "crew_id", crewID, "error", err)
	}
}
