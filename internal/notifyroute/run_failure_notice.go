package notifyroute

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

const runFailureNoticeBatch = 100

const pendingRunFailureNotices = `SELECT id,run_id,projected FROM run_failure_notice_outbox
WHERE id>(SELECT watermark FROM run_failure_notice_state WHERE id=1)
ORDER BY id LIMIT ?`

// RunFailureNotices drains the work ledger's transactional outbox on journal
// wakeups. A minute timer recovers lost wakeups and transient database errors.
func (r *Router) RunFailureNotices(ctx context.Context, isLeader func() bool) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.runFailureWake:
		case <-ticker.C:
		}
		if isLeader != nil && !isLeader() {
			continue
		}
		if err := r.drainRunFailureNotices(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("run failure notice pending", "error", err)
		}
	}
}

func (r *Router) wakeRunFailureNotices(kind journal.EntryType) {
	if kind != journal.EntryRunFailed && kind != journal.EntryRunTimeout {
		return
	}
	select {
	case r.runFailureWake <- struct{}{}:
	default:
	}
}

func (r *Router) drainRunFailureNotices(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, pendingRunFailureNotices, runFailureNoticeBatch)
	if err != nil {
		return err
	}
	type event struct {
		id        int64
		run       string
		projected bool
	}
	var pending []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.run, &e.projected); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, e := range pending {
		write, ok := quiesce.Enter(ctx)
		if !ok {
			return ctx.Err()
		}
		err := func() error {
			defer write.Leave()
			writeCtx := write.Context()
			if err := r.projectRunFailureNotice(writeCtx, e.id, e.run, e.projected); err != nil {
				return err
			}
			_, err := r.db.ExecContext(writeCtx, `UPDATE run_failure_notice_state SET watermark=max(watermark,?) WHERE id=1`, e.id)
			return err
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) projectRunFailureNotice(ctx context.Context, outboxID int64, run string, projected bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, allowed, err := resolveRunFailureNotice(ctx, tx, run)
	if err != nil || !allowed {
		return err
	}
	item := inbox.Item{
		WorkspaceID: source.WorkspaceID, Kind: inbox.KindFailedRun, SourceID: run,
		TargetUserID: source.UserID, Title: "Agent run failed", SenderType: "system",
		SenderName: "Runs", Priority: "high",
		BodyMD:  "The agent run ended unsuccessfully. Open run history to see the failure before retrying.",
		Payload: map[string]any{"subkind": "work_run_failure"},
	}
	if projected {
		var visible bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items
			WHERE workspace_id=? AND kind=? AND source_id=? AND target_user_id=?)`,
			source.WorkspaceID, item.Kind, run, source.UserID).Scan(&visible)
		if err != nil || !visible {
			return err
		}
	} else {
		if err := inbox.InsertTx(ctx, tx, r.logger, item); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_failure_notice_outbox SET projected=1 WHERE id=?`, outboxID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !projected {
		if hub, ok := r.presence.(interface {
			BroadcastChannel(prefix, id, eventType string, payload any)
		}); ok {
			hub.BroadcastChannel("user", source.UserID, "inbox.updated", map[string]string{"source": "run_failure"})
		}
	}
	// An error before a delivery row exists keeps the event behind the
	// watermark. Once enqueued, external failures retry in the delivery outbox.
	return r.routeToUser(ctx, categoryForItem(item), item, source.UserID)
}

func (r *Router) canDeliverRunFailure(ctx context.Context, workspace, run, user string) (bool, error) {
	source, allowed, err := resolveRunFailureNotice(ctx, r.db, run)
	return allowed && source.WorkspaceID == workspace && source.UserID == user, err
}
