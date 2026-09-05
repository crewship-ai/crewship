package inbox

// digest.go is the scheduler F30 names and B10 (#2364) builds: "there is no
// success digest, and the digest setting is dead" (PRD-ISSUES-AND-
// ROUTINES-2026 F30). §12 puts the digest in the Inbox's "Updates" lane —
// "the digest of successful and no-change runs" — sitting next to the two
// hard rules that motivate it: NO_CHANGE and SUCCEEDED never create an
// item on their own (I10 means they are still *recorded*, just not as a
// card demanding action), so without this sweep that history is invisible
// unless someone goes looking in the Journal.
//
// Deliberately NOT the same thing as user_notification_prefs' 'digest'
// state (internal/notifyroute/prefs.go): that column governs whether an
// EXTERNAL channel (email, Slack, webhook) batches deliveries instead of
// sending them immediately, and turning it on is a separate, larger
// project (a real batching send path for internal/notifyroute.Router,
// still stated by prefs.go as MVP-unwritten). This sweep only concerns
// what's IN the product inbox. The two are complementary, not the same
// gap, and conflating them would leave this one unfixed under the belief
// the other covers it.
//
// One card per workspace, refreshed in place via WriteThreaded rather than
// growing a new row every sweep — the same "one card, not a new one every
// morning" contract §12 asks for the routine-condition case. Represented
// as kind=message with payload.subkind="digest" (the SAME discriminator
// convention runner_notify already established for routine progress
// notices, see notify.SubkindRoutineUpdate) rather than a new inbox kind:
// no CHECK-widening migration, no inbox.AllKinds entry, no new seed row —
// the row is another "message", just one this package writes on a timer
// instead of a producer writing on an event.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// DigestSubkind is the payload discriminator this sweep writes, mirrored
// from notify.SubkindRoutineUpdate's own pattern (that constant lives in
// internal/notify, which this leaf package does not import — see the
// package doc's "stays a leaf" rule). A test
// (TestDigestSubkindMatchesNotifyConstant, internal/notify) pins the two
// spellings against drift, the same way TestRoutineUpdateSubkindMatchesProducer
// already does for the routine-update one.
const DigestSubkind = "digest"

// DefaultDigestWindow is how far back each sweep looks for SUCCEEDED/
// NO_CHANGE runs to summarize. A rolling window, not a calendar day: the
// card is refreshed in place every sweep with "what happened in the last
// 24h", so there is always exactly one current digest per workspace
// rather than a new one at midnight.
const DefaultDigestWindow = 24 * time.Hour

// DefaultDigestSweepInterval is how often RunDigestSweepOnce is invoked by
// StartDigestScheduler. Three hours: frequent enough that the card stays
// current without anyone waiting a full day to see it appear the first
// time, infrequent enough that it reads as a periodic summary rather than
// noise.
const DefaultDigestSweepInterval = 3 * time.Hour

// digestThreadKey is the stable, workspace-scoped thread every sweep for
// that workspace writes to — the merge key that keeps this to one row.
func digestThreadKey(workspaceID string) string {
	return "digest:" + workspaceID
}

// digestSourceID is the (kind, source_id) identity of the digest row.
// Stable per workspace, matching the thread_key, so WriteThreaded's
// no-existing-thread branch and its ON CONFLICT(kind, source_id) fallback
// agree on the same row across the process lifetime.
func digestSourceID(workspaceID string) string {
	return "digest:" + workspaceID
}

// digestCounts is one workspace's tally for the current window.
type digestCounts struct {
	Succeeded int
	NoChange  int
}

func (c digestCounts) total() int { return c.Succeeded + c.NoChange }

// RunDigestSweepOnce scans every workspace for SUCCEEDED/NO_CHANGE runs
// (assignments.outcome and pipeline_runs.outcome — §9.6's shared enum,
// finished within `window`) and writes or refreshes one digest card per
// workspace that has any. A workspace with nothing to report this window
// is left alone — an empty digest is not news, and would just be a card
// nobody has any reason to open.
//
// Best-effort per workspace: one workspace's write failure is logged and
// does not stop the sweep from covering the rest — the same "a sweep is a
// best-effort batch job" contract StartTimeoutSweeper's caller already
// follows.
func RunDigestSweepOnce(ctx context.Context, db *sql.DB, logger *slog.Logger, now time.Time, window time.Duration) error {
	if db == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if window <= 0 {
		window = DefaultDigestWindow
	}
	since := now.Add(-window).UTC().Format(time.RFC3339Nano)

	workspaceIDs, err := digestActiveWorkspaces(ctx, db, since)
	if err != nil {
		return fmt.Errorf("inbox: digest sweep: list workspaces: %w", err)
	}

	for _, wsID := range workspaceIDs {
		counts, err := digestCountsForWorkspace(ctx, db, wsID, since)
		if err != nil {
			logger.Warn("inbox: digest sweep: count", "workspace_id", wsID, "error", err)
			continue
		}
		if counts.total() == 0 {
			continue
		}
		if err := writeDigestItem(ctx, db, logger, wsID, counts, window); err != nil {
			logger.Warn("inbox: digest sweep: write", "workspace_id", wsID, "error", err)
		}
	}
	return nil
}

// digestActiveWorkspaces returns the distinct workspace ids with at least
// one SUCCEEDED/NO_CHANGE assignment or pipeline_run finished since
// `since`, so the sweep does not pay for a full workspaces-table scan on
// every tick.
func digestActiveWorkspaces(ctx context.Context, db *sql.DB, since string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT workspace_id FROM (
			SELECT workspace_id FROM assignments
			 WHERE outcome IN ('SUCCEEDED', 'NO_CHANGE') AND finished_at >= ?
			UNION
			SELECT workspace_id FROM pipeline_runs
			 WHERE outcome IN ('SUCCEEDED', 'NO_CHANGE') AND ended_at >= ?
		)`, since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// digestCountsForWorkspace tallies one workspace's SUCCEEDED/NO_CHANGE
// runs across both entry points (§9.6: outcome is set at two sources,
// assignments for issue/session runs and pipeline_runs for routines).
func digestCountsForWorkspace(ctx context.Context, db *sql.DB, workspaceID, since string) (digestCounts, error) {
	var c digestCounts
	err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM assignments WHERE workspace_id = ? AND outcome = 'SUCCEEDED' AND finished_at >= ?) +
			(SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = ? AND outcome = 'SUCCEEDED' AND ended_at >= ?),
			(SELECT COUNT(*) FROM assignments WHERE workspace_id = ? AND outcome = 'NO_CHANGE' AND finished_at >= ?) +
			(SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = ? AND outcome = 'NO_CHANGE' AND ended_at >= ?)
	`, workspaceID, since, workspaceID, since, workspaceID, since, workspaceID, since).Scan(&c.Succeeded, &c.NoChange)
	return c, err
}

// writeDigestItem raises or refreshes the one digest card for workspaceID.
// TargetRole is left empty (workspace-wide, like an ordinary "message" kind
// notice) — a digest is informational for anyone in the workspace, not a
// decision addressed to a role, so it carries no who_can_act beyond that.
func writeDigestItem(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string, counts digestCounts, window time.Duration) error {
	title := fmt.Sprintf("%d routine run(s) completed quietly", counts.total())
	body := fmt.Sprintf(
		"In the last %s: %d succeeded, %d needed no change. Recorded in the Journal; nothing here needs a decision.",
		window.Round(time.Minute), counts.Succeeded, counts.NoChange,
	)
	return WriteThreaded(ctx, db, logger, Item{
		WorkspaceID: workspaceID,
		Kind:        KindMessage,
		SourceID:    digestSourceID(workspaceID),
		Title:       title,
		BodyMD:      body,
		SenderType:  "system",
		SenderName:  "Digest",
		Priority:    "low",
		Blocking:    false,
		Payload: map[string]any{
			"subkind":        DigestSubkind,
			"succeeded":      counts.Succeeded,
			"no_change":      counts.NoChange,
			"window_seconds": int(window.Seconds()),
		},
		ThreadKey:      digestThreadKey(workspaceID),
		AttentionClass: AttentionReview,
		Actions: []Action{
			{ID: "view", Label: "View recent activity", Effect: "Opens the Journal filtered to this window", Irreversible: false},
		},
	})
}

// StartDigestScheduler starts the periodic sweep and returns immediately —
// the sweep itself runs in a background goroutine tied to ctx, exactly the
// shape harbormaster.StartTimeoutSweeper and ephemeral.StartExpirySweeper
// already use (§16.1's "sweepers are a solved shape here", F48: do not add
// a third scheduler shape). Cancel ctx to stop it; there is no separate
// stop handle because none of this codebase's sweepers have one — they
// stop when the server's root context does.
func StartDigestScheduler(ctx context.Context, db *sql.DB, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultDigestSweepInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := RunDigestSweepOnce(ctx, db, logger, time.Now(), DefaultDigestWindow); err != nil {
					logger.Warn("inbox: digest sweep failed", "error", err)
				}
			}
		}
	}()
}
