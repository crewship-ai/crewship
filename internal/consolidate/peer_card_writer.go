package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — PeerCardSync auxiliary writer.
//
// This is the heart of the peer-card extraction loop: every daily
// (or operator-triggered) PeerCardSync routine run walks the
// (agent_id, opener_user_id) pairs that crossed the interaction
// threshold since the last sweep and asks the aux LLM slot to
// extract / refresh the peer card for each.
//
// MVP behaviour deliberately defers the aux-LLM extraction call to
// the wiring layer (internal/scheduler PeerCardSync routine handler
// in PR-E.8). This package owns the deterministic plumbing:
//
//   - Threshold check (≥10 messages OR ≥5 minutes session)
//   - user_peer_consent gate (opted-out users skipped + purged)
//   - DB index upkeep (peer_cards INSERT / DELETE)
//   - Audit log emission (peer_card_audit per write / delete)
//   - On-disk persistence via memory.WritePeerCard / DeletePeerCard
//
// The extraction prompt + LLM call live in the routine handler so
// this package stays free of LLM client dependencies — keeps the
// test surface fast and the package boundary clean.

// PeerCardThreshold is the minimum interaction signal required
// before a (agent, user) pair gets a peer card written. Either bar
// satisfies — a fast-moving 12-message exchange counts even at 90
// seconds elapsed; a slow 3-message exchange counts at 5+ minutes.
type PeerCardThreshold struct {
	MinMessages int
	MinSession  time.Duration
}

// DefaultPeerCardThreshold is the PRD-documented "≥10 messages OR
// ≥5 minutes". Operators can tighten via the routine config (the
// public threshold lives behind the routine handler so a future
// per-workspace override is a config plumb, not a code change).
var DefaultPeerCardThreshold = PeerCardThreshold{
	MinMessages: 10,
	MinSession:  5 * time.Minute,
}

// MeetsThreshold reports whether the candidate pair has accumulated
// enough interaction to warrant a card. The OR semantics match the
// PRD; using AND here would suppress cards for short bursty users
// even when they've sent enough messages to be reliably profiled.
func (t PeerCardThreshold) MeetsThreshold(messageCount int, sessionDuration time.Duration) bool {
	return messageCount >= t.MinMessages || sessionDuration >= t.MinSession
}

// PeerCandidate is one (agent, user) pair the sweep is considering.
// Populated by the routine handler from a JOIN over chats +
// chat_messages restricted to chats opened by the user.
type PeerCandidate struct {
	WorkspaceID     string
	AgentID         string
	AgentSlug       string
	UserID          string
	MessageCount    int
	SessionDuration time.Duration
}

// IsOptedOut probes user_peer_consent. The query is bounded to the
// (user, workspace) PK so it's O(1) per candidate. Errors are
// surfaced rather than silently defaulting because a flaky DB
// should not cause a stealth opt-out bypass.
func IsOptedOut(ctx context.Context, db *sql.DB, userID, workspaceID string) (bool, error) {
	var optedOut int
	err := db.QueryRowContext(ctx, `
		SELECT opted_out FROM user_peer_consent
		WHERE user_id = ? AND workspace_id = ?
	`, userID, workspaceID).Scan(&optedOut)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("peer consent probe: %w", err)
	}
	return optedOut == 1, nil
}

// PeerSyncOutcome is the per-candidate result a routine handler
// hands back to the scheduler for journal emission + metrics.
type PeerSyncOutcome struct {
	UserID   string
	AgentID  string
	UserSlug string
	Action   string // "write" | "skip_threshold" | "skip_opt_out" | "delete_opt_out" | "skip_empty_content"
	Bytes    int
	Reason   string
	Err      error
}

// SyncPeerCard runs the per-candidate decision loop:
//
//  1. Opt-out check  → if true and a card exists, delete + audit; return.
//  2. Threshold check → if not met, skip with reason.
//  3. content == ""   → caller (the LLM-driven extractor in the
//     routine handler) decided there's nothing to write yet;
//     skip with reason. Distinguished from opt-out for metrics.
//  4. Otherwise        → WritePeerCard + INSERT/UPDATE peer_cards
//     row + emit peer_card_audit 'write'.
//
// The function does NOT call the LLM. The routine handler shapes
// the content via the aux model slot and passes the resulting text
// in via the `content` parameter so this primitive stays unit-
// testable without a live model.
func SyncPeerCard(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	threshold PeerCardThreshold,
	cand PeerCandidate,
	content string,
	paths memory.PeerPaths,
	now time.Time,
) PeerSyncOutcome {
	slug := memory.UserSlug(cand.UserID, cand.WorkspaceID)
	out := PeerSyncOutcome{UserID: cand.UserID, AgentID: cand.AgentID, UserSlug: slug}

	// 1) consent
	optedOut, err := IsOptedOut(ctx, db, cand.UserID, cand.WorkspaceID)
	if err != nil {
		out.Action = "skip_opt_out"
		out.Err = err
		return out
	}
	if optedOut {
		// Purge any existing card. Opt-out is a hard stop, not a
		// "we'll get to it eventually" — keeping a card around
		// after the user opted out is a GDPR violation by itself.
		// Propagate file + DB errors so the routine summary counts
		// these as failures (otherwise GDPR purge looks complete
		// when disk/DB cleanup actually failed).
		if err := memory.DeletePeerCard(paths, cand.UserID, cand.WorkspaceID); err != nil {
			out.Action = "delete_opt_out"
			out.Err = fmt.Errorf("delete peer card on opt-out: %w", err)
			return out
		}
		// Remove the index row too; the disk-mirror should match.
		if _, err := db.ExecContext(ctx, `
			DELETE FROM peer_cards
			WHERE agent_id = ? AND user_slug = ?
		`, cand.AgentID, slug); err != nil {
			out.Action = "delete_opt_out"
			out.Err = fmt.Errorf("delete peer_cards index on opt-out: %w", err)
			return out
		}
		recordAudit(ctx, db, logger, peerAuditRow{
			workspaceID:  cand.WorkspaceID,
			actorKind:    "system",
			action:       "delete",
			targetUserID: cand.UserID,
			agentID:      cand.AgentID,
			metadata:     `{"reason":"opt_out"}`,
		})
		out.Action = "delete_opt_out"
		out.Reason = "user opted out"
		return out
	}

	// 2) threshold
	if !threshold.MeetsThreshold(cand.MessageCount, cand.SessionDuration) {
		out.Action = "skip_threshold"
		out.Reason = fmt.Sprintf("messages=%d session=%s below %d/%s",
			cand.MessageCount, cand.SessionDuration,
			threshold.MinMessages, threshold.MinSession)
		return out
	}

	// 3) extractor produced nothing yet
	if strings.TrimSpace(content) == "" {
		out.Action = "skip_empty_content"
		out.Reason = "extractor returned empty body"
		return out
	}

	// 4) write
	if err := memory.WritePeerCard(paths, cand.UserID, cand.WorkspaceID, content); err != nil {
		out.Action = "write"
		out.Err = fmt.Errorf("WritePeerCard: %w", err)
		return out
	}
	// Upsert the index row. The UNIQUE(agent_id, user_slug)
	// constraint plus ON CONFLICT DO UPDATE preserves the original
	// id (so audit join paths stay stable) while bumping bytes +
	// updated_at on each refresh.
	cardID := stableCardID(cand.AgentID, slug)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, user_slug) DO UPDATE SET
		    bytes = excluded.bytes,
		    updated_at = excluded.updated_at
	`, cardID, cand.WorkspaceID, cand.AgentID, cand.UserID, slug,
		paths.CardPath(slug), len(content),
		now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano)); err != nil {
		// Propagate the upsert failure — the disk file is now ahead
		// of the index, which the next routine sweep can re-sync,
		// but reporting "write" success here would hide the drift
		// from monitoring + the routine summary.
		out.Action = "write"
		out.Err = fmt.Errorf("peer_cards upsert: %w", err)
		return out
	}
	recordAudit(ctx, db, logger, peerAuditRow{
		workspaceID:  cand.WorkspaceID,
		actorKind:    "system",
		action:       "write",
		targetUserID: cand.UserID,
		agentID:      cand.AgentID,
		metadata:     fmt.Sprintf(`{"bytes":%d}`, len(content)),
	})
	out.Action = "write"
	out.Bytes = len(content)
	return out
}

// peerAuditRow + recordAudit are package-private rather than living
// on the wider audit_log surface because peer_card_audit is keyed on
// target_user_id (data subject), not user_id (actor). The audit_log
// path can't represent that shape without metadata-JSON gymnastics.
type peerAuditRow struct {
	workspaceID  string
	actorUserID  string
	actorKind    string
	action       string
	targetUserID string
	agentID      string
	metadata     string
}

func recordAudit(ctx context.Context, db *sql.DB, logger *slog.Logger, row peerAuditRow) {
	var actorUser any
	if row.actorUserID != "" {
		actorUser = row.actorUserID
	}
	var agentID any
	if row.agentID != "" {
		agentID = row.agentID
	}
	var meta any
	if row.metadata != "" {
		meta = row.metadata
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO peer_card_audit
		(id, workspace_id, actor_user_id, actor_kind, action, target_user_id, agent_id, metadata)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)
	`, row.workspaceID, actorUser, row.actorKind, row.action,
		row.targetUserID, agentID, meta)
	if err != nil {
		logger.Warn("peer_card_audit insert failed",
			"action", row.action, "target", row.targetUserID, "err", err)
	}
}

// stableCardID produces a deterministic CUID-ish identifier from
// (agent_id, user_slug) so two routine runs landing concurrently
// can't insert duplicate rows. The hash is short (16 hex chars +
// the "pc_" prefix) and never carries PII.
func stableCardID(agentID, userSlug string) string {
	return "pc_" + memory.UserSlug(agentID, userSlug)
}
