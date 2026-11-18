package notifyroute

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

const runFailureNoticeBatch = 100

const pendingRunFailureNotices = `SELECT id,run_id FROM run_failure_notice_outbox
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
	write, ok := quiesce.Enter(ctx)
	if !ok {
		return ctx.Err()
	}
	defer write.Leave()
	rows, err := r.db.QueryContext(ctx, pendingRunFailureNotices, runFailureNoticeBatch)
	if err != nil {
		return err
	}
	type event struct {
		id  int64
		run string
	}
	var pending []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.run); err != nil {
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
		if err := r.projectRunFailureNotice(ctx, e.run); err != nil {
			return err
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE run_failure_notice_state SET watermark=max(watermark,?) WHERE id=1`, e.id); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) projectRunFailureNotice(ctx context.Context, run string) error {
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
		BodyMD: "The agent run ended unsuccessfully. Open run history to see the failure before retrying.",
		Payload: map[string]any{"subkind": "work_run_failure", "run_id": run,
			"agent_id": source.AgentID, "crew_id": source.CrewID},
	}
	if err := inbox.InsertTx(ctx, tx, r.logger, item); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if hub, ok := r.presence.(interface {
		BroadcastChannel(prefix, id, eventType string, payload any)
	}); ok {
		hub.BroadcastChannel("user", source.UserID, "inbox.updated", map[string]string{"source": "run_failure"})
	}
	// An error before a delivery row exists keeps the event behind the
	// watermark. Once enqueued, external failures retry in the delivery outbox.
	return r.routeToUser(ctx, categoryForItem(item), item, source.UserID)
}

func (r *Router) canDeliverRunFailure(ctx context.Context, workspace, run, user string) (bool, error) {
	source, allowed, err := resolveRunFailureNotice(ctx, r.db, run)
	return allowed && source.WorkspaceID == workspace && source.UserID == user, err
}
