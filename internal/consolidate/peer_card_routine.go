package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — PeerCardSync daily routine.
//
// Wraps the per-candidate SyncPeerCard primitive in a workspace-
// wide sweep so a single tick covers every (agent, opener_user_id)
// pair eligible for a refresh.
//
// MVP behaviour: the extractor is a stub that returns the existing
// card content (no LLM call), so the routine effectively acts as
// a "purge cards for opted-out users" + "create empty cards for
// threshold-crossing users we don't have one for yet" pass. The
// real aux-LLM extractor lands in PR-F (or a Phase 2 followup) and
// plugs in via the PeerExtractor interface below.

// PeerExtractor produces the markdown body for a peer card given
// the (agent, user) interaction signal. Implementations may call
// the aux LLM slot, run a deterministic summariser, or no-op. The
// returned content is subject to the standard peer card cap; the
// routine truncates if needed and emits a journal entry when it
// does so the operator notices the extractor producing oversize
// output.
//
// Return ("", nil) when there's nothing meaningful to say yet —
// SyncPeerCard treats empty content as skip_empty_content rather
// than a write of empty bytes.
type PeerExtractor interface {
	Extract(ctx context.Context, cand PeerCandidate) (string, error)
}

// NoopExtractor is the MVP placeholder. Returns empty content so
// the routine sweep purges + indexes without writing new cards.
// Wire a real extractor (aux-LLM driven) via WithPeerExtractor
// option when ready.
type NoopExtractor struct{}

func (NoopExtractor) Extract(_ context.Context, _ PeerCandidate) (string, error) {
	return "", nil
}

// PeerCardSyncOptions parameterises the routine. OutputBasePath
// must match the host-side cfg.Storage.BasePath the container
// provider uses for bind mounts.
type PeerCardSyncOptions struct {
	OutputBasePath string
	Threshold      PeerCardThreshold
	Extractor      PeerExtractor
	// LookbackWindow bounds the chats query so a multi-year-old
	// chat with one stale message doesn't trigger a peer card
	// extraction today. Defaults to 14 days.
	LookbackWindow time.Duration
}

// RunPeerCardSync is the routine entry point. Walks every active
// (agent, opener_user_id) pair across the workspace that meets the
// interaction threshold and either writes a fresh card (auto-
// approved path; requires consent != opted_out) or purges an
// existing card (opt-out path; runs unconditionally).
//
// Returns a per-outcome counter for journal emission. Logs (rather
// than returns) per-candidate failures so a single bad agent
// doesn't poison the whole sweep.
type PeerSyncSummary struct {
	WorkspaceID   string
	Candidates    int
	Writes        int
	SkippedThresh int
	SkippedEmpty  int
	SkippedOptOut int // user opted out but had no card to purge
	PurgedOptOut  int // user opted out AND we deleted an existing card
	Errors        int
}

func RunPeerCardSync(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	workspaceID string,
	opts PeerCardSyncOptions,
) (PeerSyncSummary, error) {
	if opts.Extractor == nil {
		opts.Extractor = NoopExtractor{}
	}
	if opts.Threshold == (PeerCardThreshold{}) {
		opts.Threshold = DefaultPeerCardThreshold
	}
	if opts.LookbackWindow <= 0 {
		opts.LookbackWindow = 14 * 24 * time.Hour
	}
	if opts.OutputBasePath == "" {
		return PeerSyncSummary{WorkspaceID: workspaceID},
			fmt.Errorf("peer card routine: OutputBasePath required")
	}

	cands, err := loadPeerCandidates(ctx, db, workspaceID, opts.LookbackWindow)
	if err != nil {
		return PeerSyncSummary{WorkspaceID: workspaceID},
			fmt.Errorf("run peer card sync: load candidates for workspace %s: %w", workspaceID, err)
	}
	sum := PeerSyncSummary{WorkspaceID: workspaceID, Candidates: len(cands)}
	now := time.Now()
	for _, cand := range cands {
		paths := memory.PeerPaths{
			AgentDir: filepath.Join(opts.OutputBasePath, "crews", cand.crewID, "agents", cand.AgentSlug, ".memory"),
		}
		content, eerr := opts.Extractor.Extract(ctx, cand.PeerCandidate)
		if eerr != nil {
			logger.Warn("peer extractor failed", "agent_id", cand.AgentID,
				"user_id", cand.UserID, "err", eerr)
			sum.Errors++
			continue
		}
		out := SyncPeerCard(ctx, db, logger, opts.Threshold, cand.PeerCandidate, content, paths, now)
		// Any outcome with a non-nil Err is a failure — including
		// consent-probe failures that surface as Action="skip_opt_out".
		// Counting them in sum.Errors keeps the routine summary
		// honest; otherwise transient DB hiccups disappear from
		// metrics.
		if out.Err != nil {
			sum.Errors++
			logger.Warn("peer card sync candidate failed",
				"agent_id", cand.AgentID, "user_id", cand.UserID,
				"action", out.Action, "err", out.Err)
			continue
		}
		switch out.Action {
		case "write":
			sum.Writes++
		case "skip_threshold":
			sum.SkippedThresh++
		case "skip_empty_content":
			sum.SkippedEmpty++
		case "skip_opt_out":
			// Clean opt-out skip (no error). Tracked separately from
			// purge events so metrics can distinguish "user is opted
			// out, nothing to do" from "user opted out and we just
			// deleted their card".
			sum.SkippedOptOut++
		case "delete_opt_out":
			sum.PurgedOptOut++
		}
	}
	return sum, nil
}

// internalPeerCandidate adds the crew_id (only needed for host-path
// resolution) so the public PeerCandidate stays free of bind-mount
// concerns.
type internalPeerCandidate struct {
	PeerCandidate
	crewID string
}

// loadPeerCandidates joins chats + agents + (aggregated chat_messages
// count) to produce the per-pair signal. Restricted to chats opened
// by a non-NULL user within the lookback window.
//
// Note: this query intentionally uses a per-chat aggregate rather
// than a workspace-wide one — operator-by-operator profiles are
// the goal, not "this user has talked to ALL my agents". A user
// who opened 5 chats with the same agent shows up once with the
// summed message count, not five times.
func loadPeerCandidates(ctx context.Context, db *sql.DB, workspaceID string, lookback time.Duration) ([]internalPeerCandidate, error) {
	since := time.Now().UTC().Add(-lookback).Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT
		    a.id              AS agent_id,
		    a.slug            AS agent_slug,
		    a.crew_id         AS crew_id,
		    c.created_by      AS user_id,
		    SUM(COALESCE(c.message_count, 0))           AS message_count,
		    MIN(c.started_at) AS first_seen,
		    MAX(COALESCE(c.ended_at, c.started_at))     AS last_seen
		FROM chats c
		JOIN agents a ON a.id = c.agent_id
		WHERE c.workspace_id = ?
		  AND c.created_by IS NOT NULL
		  AND c.started_at >= ?
		  AND a.deleted_at IS NULL
		GROUP BY a.id, a.slug, a.crew_id, c.created_by
	`, workspaceID, since)
	if err != nil {
		return nil, fmt.Errorf("peer candidate query: %w", err)
	}
	defer rows.Close()
	var out []internalPeerCandidate
	for rows.Next() {
		var (
			agentID, agentSlug, userID string
			crewID                     sql.NullString
			msgCount                   int
			firstSeen, lastSeen        string
		)
		if err := rows.Scan(&agentID, &agentSlug, &crewID, &userID, &msgCount, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan peer candidate (workspace %s): %w", workspaceID, err)
		}
		dur := parseSessionDuration(firstSeen, lastSeen)
		out = append(out, internalPeerCandidate{
			PeerCandidate: PeerCandidate{
				WorkspaceID:     workspaceID,
				AgentID:         agentID,
				AgentSlug:       agentSlug,
				UserID:          userID,
				MessageCount:    msgCount,
				SessionDuration: dur,
			},
			crewID: crewID.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peer candidates (workspace %s): %w", workspaceID, err)
	}
	return out, nil
}

func parseSessionDuration(first, last string) time.Duration {
	f, ferr := time.Parse(time.RFC3339, first)
	l, lerr := time.Parse(time.RFC3339, last)
	if ferr != nil || lerr != nil {
		return 0
	}
	d := l.Sub(f)
	if d < 0 {
		return 0
	}
	return d
}
