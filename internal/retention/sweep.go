package retention

// Sweeps for the three windows this package introduced: inbox items, chats and
// Keeper decisions. Each runs only for a workspace whose window is set — a
// workspace with no retention_settings row, or a NULL one, keeps everything.
//
// Each window has ONE eligibility predicate, used both by its sweep and by
// Count, so the number the console shows before a save is the number the next
// sweep deletes (give or take what ages in or changes meanwhile).
//
// What "open" means, and is therefore never deleted:
//
//   Inbox — anything unread, and anything read that still blocks (a read
//   approval nobody answered is still an open question). Only resolved items,
//   and read items with blocking = 0, age out, by when they were resolved or
//   read.
//
//   Chats — a chat is eligible once its last activity is older than the window
//   and it holds no open work. Open work, conservatively:
//     - any delegation (assignments row) — the same rule that makes a manual
//       chat delete answer 409, because a delegation is audit trail;
//     - an escalation raised in it that nobody resolved;
//     - an unresolved "agent replied" inbox item for it;
//     - a pending approval whose payload names it;
//     - a routine run it belongs to that has not finished;
//     - an access grant for it, not revoked, issued inside the window.
//   Deleting a chat removes its messages, read cursors and resolved reply
//   items with it, as the manual delete does; the rest cascades. Attachment
//   rows cascade and the hourly attachment collector reclaims their blobs.
//
//   Keeper decisions — only a request with a verdict (not NULL, PENDING or
//   ESCALATE) ages out, by decided_at. A request that backs a credential lease
//   still live is kept. Its keeper_request_events ledger rows go with it. An
//   unconsumed human approval is spendable for 15 minutes, far inside the
//   one-day minimum window, so it can never be swept while spendable.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const (
	// sweepBatch bounds one DELETE / one transaction, and sweepMaxBatches one
	// workspace per tick, for the reason the audit sweep gives: SQLite holds
	// the one write lock for the whole statement. A backlog past the cap is
	// logged and continues on the next tick.
	sweepBatch      = 1000
	sweepMaxBatches = 100
)

func cutoff(now time.Time, d int) string {
	return tsformat.Format(now.Add(-time.Duration(d) * 24 * time.Hour).UTC())
}

// inboxEligible: args workspace_id, cutoff.
const inboxEligible = `workspace_id = ?
	AND (state = 'resolved' OR (state = 'read' AND blocking = 0))
	AND COALESCE(resolved_at, read_at, updated_at, created_at) < ?`

// chatEligible: args workspace_id, cutoff, cutoff.
const chatEligible = `c.workspace_id = ?
	AND COALESCE(c.last_activity_at, c.updated_at, c.created_at) < ?
	AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.chat_id = c.id)
	AND NOT EXISTS (SELECT 1 FROM escalations e WHERE e.chat_id = c.id AND e.resolved_at IS NULL)
	AND NOT EXISTS (SELECT 1 FROM inbox_items i
	                 WHERE i.workspace_id = c.workspace_id AND i.state <> 'resolved'
	                   AND substr(i.source_id, 1, length('chat_reply_' || c.id || '_')) = 'chat_reply_' || c.id || '_')
	AND NOT EXISTS (SELECT 1 FROM approvals_queue q
	                 WHERE q.workspace_id = c.workspace_id AND q.status = 'pending'
	                   AND json_valid(q.payload) AND json_extract(q.payload, '$.chat_id') = c.id)
	AND NOT EXISTS (SELECT 1 FROM pipeline_runs r
	                 WHERE r.id = c.pipeline_run_id
	                   AND r.status NOT IN ('completed', 'failed', 'cancelled', 'interrupted', 'dry_run'))
	AND NOT EXISTS (SELECT 1 FROM access_attempts x
	                 WHERE x.chat_id = c.id AND x.revoked_at IS NULL AND x.created_at >= ?)`

// keeperEligible: args workspace_id, workspace_id, cutoff. A request belongs
// to a workspace through its agent or through its ledger, the same two routes
// the manual Keeper purge uses (keeper_requests has no workspace_id).
const keeperEligible = `(kr.requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?)
	     OR kr.id IN (SELECT request_id FROM keeper_request_events WHERE workspace_id = ?))
	AND kr.decision IS NOT NULL AND kr.decision NOT IN ('PENDING', 'ESCALATE')
	AND COALESCE(kr.decided_at, kr.created_at) < ?
	AND NOT EXISTS (SELECT 1 FROM agent_credentials ac
	                 WHERE ac.lease_request_id = kr.id
	                   AND (ac.expires_at IS NULL OR julianday(ac.expires_at) IS NULL
	                        OR julianday(ac.expires_at) > julianday('now')))`

// SweepInbox deletes one workspace's aged resolved or read-and-not-blocking
// inbox items. days <= 0 deletes nothing.
func SweepInbox(ctx context.Context, db *sql.DB, workspaceID string, d int, now time.Time) (int, bool, error) {
	if d <= 0 || workspaceID == "" {
		return 0, false, nil
	}
	c := cutoff(now, d)
	deleted := 0
	for i := 0; i < sweepMaxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return deleted, false, err
		}
		if err := quiesce.Yield(ctx); err != nil {
			return deleted, false, err
		}
		res, err := db.ExecContext(ctx,
			`DELETE FROM inbox_items WHERE id IN (SELECT id FROM inbox_items WHERE `+inboxEligible+` LIMIT ?)`,
			workspaceID, c, sweepBatch)
		if err != nil {
			return deleted, false, fmt.Errorf("retention: sweep inbox for %s: %w", workspaceID, err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
		if n < sweepBatch {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

// SweepChats deletes one workspace's idle chats that hold no open work.
func SweepChats(ctx context.Context, db *sql.DB, workspaceID string, d int, now time.Time) (int, bool, error) {
	if d <= 0 || workspaceID == "" {
		return 0, false, nil
	}
	c := cutoff(now, d)
	deleted := 0
	for i := 0; i < sweepMaxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return deleted, false, err
		}
		if err := quiesce.Yield(ctx); err != nil {
			return deleted, false, err
		}
		n, err := sweepChatBatch(ctx, db, workspaceID, c)
		deleted += n
		if err != nil {
			return deleted, false, fmt.Errorf("retention: sweep chats for %s: %w", workspaceID, err)
		}
		if n < sweepBatch {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

func sweepChatBatch(ctx context.Context, db *sql.DB, workspaceID, c string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := selectIDs(ctx, tx, `SELECT c.id FROM chats c WHERE `+chatEligible+` LIMIT ?`, workspaceID, c, c, sweepBatch)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	for _, id := range ids {
		for _, stmt := range []struct {
			q    string
			args []any
		}{
			{`DELETE FROM conversation_messages WHERE session_id = ?`, []any{id}},
			{`DELETE FROM chat_read_cursors WHERE chat_id = ?`, []any{id}},
			{`DELETE FROM inbox_items WHERE workspace_id = ? AND substr(source_id, 1, length(?)) = ?`,
				[]any{workspaceID, "chat_reply_" + id + "_", "chat_reply_" + id + "_"}},
			{`DELETE FROM chats WHERE id = ?`, []any{id}},
		} {
			if _, err := tx.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// SweepKeeperDecisions deletes one workspace's aged, decided Keeper requests
// and their ledger rows.
func SweepKeeperDecisions(ctx context.Context, db *sql.DB, workspaceID string, d int, now time.Time) (int, bool, error) {
	if d <= 0 || workspaceID == "" {
		return 0, false, nil
	}
	c := cutoff(now, d)
	deleted := 0
	for i := 0; i < sweepMaxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return deleted, false, err
		}
		if err := quiesce.Yield(ctx); err != nil {
			return deleted, false, err
		}
		n, err := sweepKeeperBatch(ctx, db, workspaceID, c)
		deleted += n
		if err != nil {
			return deleted, false, fmt.Errorf("retention: sweep keeper decisions for %s: %w", workspaceID, err)
		}
		if n < sweepBatch {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

func sweepKeeperBatch(ctx context.Context, db *sql.DB, workspaceID, c string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := selectIDs(ctx, tx, `SELECT kr.id FROM keeper_requests kr WHERE `+keeperEligible+` LIMIT ?`,
		workspaceID, workspaceID, c, sweepBatch)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	if _, err := tx.ExecContext(ctx, `DELETE FROM keeper_request_events WHERE request_id IN (`+ph+`)`, args...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM keeper_requests WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func selectIDs(ctx context.Context, db DB, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SweepAll runs every set inbox, chats and Keeper decisions window. Errors are
// collected, not returned on the first, so one workspace cannot stop the rest.
func SweepAll(ctx context.Context, db *sql.DB, logger *slog.Logger, now time.Time) error {
	// Never delete under an instance backup's consistent copy: the whole sweep
	// is a writer in the quiet window's barrier, and it yields between
	// windows and batches so a backup's drain never waits on it.
	wr, err := quiesce.EnterWait(ctx)
	if err != nil {
		return err
	}
	defer wr.Leave()
	ctx = wr.Context()
	if db == nil {
		return errors.New("retention: sweep: db is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	rows, err := db.QueryContext(ctx, `
		SELECT rs.workspace_id, rs.key, rs.days FROM retention_settings rs
		  JOIN workspaces w ON w.id = rs.workspace_id
		 WHERE rs.days IS NOT NULL AND rs.days > 0
		 ORDER BY rs.workspace_id, rs.key`)
	if err != nil {
		return fmt.Errorf("retention: list windows: %w", err)
	}
	type job struct {
		ws   string
		key  Key
		days int
	}
	var jobs []job
	for rows.Next() {
		var j job
		var k string
		if err := rows.Scan(&j.ws, &k, &j.days); err != nil {
			rows.Close()
			return fmt.Errorf("retention: scan window: %w", err)
		}
		j.key = Key(k)
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("retention: list windows: %w", err)
	}
	var errs []error
	for _, j := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := wr.Yield(ctx); err != nil {
			return err
		}
		var sweep func(context.Context, *sql.DB, string, int, time.Time) (int, bool, error)
		switch j.key {
		case Inbox:
			sweep = SweepInbox
		case Chats:
			sweep = SweepChats
		case KeeperDecisions:
			sweep = SweepKeeperDecisions
		default:
			continue
		}
		n, capped, err := sweep(ctx, db, j.ws, j.days, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if n > 0 {
			logger.Info("retention sweep", "window", string(j.key), "workspace_id", j.ws, "deleted", n, "retention_days", j.days)
		}
		if capped {
			logger.Warn("retention sweep stopped at its batch cap", "window", string(j.key), "workspace_id", j.ws,
				"deleted", n, "note", "the backlog continues on the next sweep")
		}
	}
	return errors.Join(errs...)
}

// StartSweeper runs SweepAll now and then every interval (daily when <= 0).
// Blocks; run it as a goroutine. Not leader-gated, like the other retention
// sweepers: the deletes are idempotent.
func StartSweeper(ctx context.Context, db *sql.DB, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := SweepAll(ctx, db, logger, time.Now()); err != nil {
		logger.Warn("retention sweeper: initial sweep failed", "error", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := SweepAll(ctx, db, logger, time.Now()); err != nil {
				logger.Warn("retention sweeper: tick failed", "error", err)
			}
		}
	}
}
