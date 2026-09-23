package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

// acceptScheduled is the only callback registered with cron for agent work.
// Acceptance commits the immutable occurrence and advances the cursor; the
// dispatcher, including on a later process, owns every execution attempt.
func (s *Scheduler) acceptScheduled(ag scheduledAgent) {
	if !s.isLeader() {
		return
	}
	if s.acceptor == nil || s.diskGuard == nil {
		s.logger.Error("scheduled work cannot be accepted: durable admission is not configured", "agent_id", ag.ID)
		return
	}
	// A large set of schedules can fire on the same minute. Bound their
	// database admission attempts so cron cannot create an unbounded burst
	// of goroutines all waiting on SQLite's single writer.
	release, ok := s.acquireDispatchSlot()
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(s.ctx, work.DefaultAcceptanceBudget+time.Second)
	defer cancel()
	receipt, err := AcceptDue(ctx, s.acceptor, s.diskGuard, ag.Workspace, ag.ID, s.nowFn(), work.DefaultIngressLimits())
	if errors.Is(err, ErrScheduleNotDue) || errors.Is(err, ErrScheduleUnavailable) {
		return
	}
	if err != nil {
		s.logger.Error("scheduled work acceptance failed; occurrence remains due", "agent_id", ag.ID, "error", err)
		return
	}
	s.logger.Info("scheduled occurrence accepted", "agent_id", ag.ID, "work_id", receipt.WorkID, "duplicate", receipt.Duplicate)
	if s.dispatchHint != nil {
		s.dispatchHint(receipt.WorkID)
	}
}

// runOverdueSweep checks at boot and after leadership changes. A follower can
// become leader long after its own Start; a one-shot boot check would leave a
// missed daily occurrence untouched until tomorrow's cron tick.
func (s *Scheduler) runOverdueSweep() {
	interval := s.overdueSweepInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.acceptOverdue()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// acceptOverdue closes the gap between a persisted due instant and the next
// wall-clock cron tick. Claiming remains atomic in AcceptDue, so this sweep
// and a simultaneous cron tick or replica cannot create two work items.
func (s *Scheduler) acceptOverdue() {
	if !s.isLeader() || s.acceptor == nil {
		return
	}
	rows, err := s.db.QueryContext(s.ctx, `SELECT id, workspace_id, COALESCE(schedule_next_run,'')
		FROM agents WHERE schedule_enabled=1 AND schedule_cron IS NOT NULL
		AND schedule_cron != '' AND deleted_at IS NULL`)
	if err != nil {
		s.logger.Error("scheduled boot sweep could not list occurrences", "error", err)
		return
	}
	type dueAgent struct{ id, workspace, due string }
	var candidates []dueAgent
	for rows.Next() {
		var a dueAgent
		if err := rows.Scan(&a.id, &a.workspace, &a.due); err != nil {
			s.logger.Error("scheduled boot sweep could not read occurrence", "error", err)
			continue
		}
		candidates = append(candidates, a)
	}
	if err := rows.Err(); err != nil {
		s.logger.Error("scheduled boot sweep incomplete", "error", err)
	}
	_ = rows.Close()
	now := s.nowFn()
	for _, a := range candidates {
		if s.ctx.Err() != nil {
			return
		}
		due, err := time.Parse(time.RFC3339Nano, a.due)
		if err != nil {
			s.logger.Error("scheduled boot sweep found invalid occurrence identity", "agent_id", a.id, "error", err)
			continue
		}
		if !due.After(now) {
			s.acceptScheduled(scheduledAgent{ID: a.id, Workspace: a.workspace})
		}
	}
}

// InitializeMissingCursors is a one-time upgrade step, before cron starts.
// Old installations may have enabled schedules with a NULL cursor. An absent
// cursor is not a due identity; guessing one inside AcceptDue would make a
// transient read failure capable of minting a second occurrence. Initialize
// only the missing rows to the next future tick and leave existing due values
// untouched for atomic acceptance.
func (s *Scheduler) InitializeMissingCursors(ctx context.Context, now time.Time) error {
	if s.db == nil {
		return errors.New("scheduler: no database for cursor initialization")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, schedule_cron FROM agents
		WHERE schedule_enabled = 1 AND schedule_cron IS NOT NULL AND schedule_cron != ''
		AND COALESCE(schedule_next_run, '') = '' AND deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("scheduler: list schedules without a cursor: %w", err)
	}
	type missing struct{ id, expr string }
	var pending []missing
	for rows.Next() {
		var m missing
		if err := rows.Scan(&m.id, &m.expr); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, m)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, m := range pending {
		plan, err := s.parser.Parse(m.expr)
		if err != nil {
			// A malformed stored schedule must not take the entire API down.
			// loadSchedules also refuses to register it; leave its cursor
			// absent and make the individual defect visible in the log.
			s.logger.Error("scheduled cursor not initialized: invalid cron", "agent_id", m.id, "error", err)
			continue
		}
		next := plan.Next(now).UTC()
		if next.IsZero() {
			return fmt.Errorf("scheduler: schedule %s has no next occurrence", m.id)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE agents SET schedule_next_run = ?
			WHERE id = ? AND COALESCE(schedule_next_run, '') = '' AND schedule_cron = ? AND schedule_enabled = 1`,
			next.Format(time.RFC3339), m.id, m.expr); err != nil {
			return fmt.Errorf("scheduler: initialize cursor for %s: %w", m.id, err)
		}
	}
	return nil
}
