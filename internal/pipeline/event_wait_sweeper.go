package pipeline

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/leader"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// StartEventWaitSweeper recovers resolved/expired parked subscriptions, with
// an immediate startup pass. Resolution stays durable until the normal resume
// path completes, so a failed scan, busy slot, or restart cannot lose a wake.
// The returned stop function cancels and joins BOTH the ticker and its workers;
// call it before closing the DB/journal. At most eight admission attempts run
// at once; admitted executions retain their normal pipeline concurrency limits
// and are all tracked and joined without holding an admission permit.
func StartEventWaitSweeper(parent context.Context, db *sql.DB, exec *Executor, gate leader.Gate, logger *slog.Logger, interval time.Duration) func() {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var executions sync.WaitGroup
		defer executions.Wait()
		var mu sync.Mutex
		active := make(map[string]bool)
		admissions := make(chan struct{}, 8)
		cursor := ""
		sweep := func() {
			if ctx.Err() != nil || (gate != nil && !gate.IsLeader()) {
				return
			}
			rows, err := db.QueryContext(ctx, `
SELECT w.run_id FROM pipeline_signal_waits w JOIN pipeline_runs r ON r.id=w.run_id
WHERE r.status='waiting' AND r.current_step_id=w.step_id
  AND w.status='pending' AND w.timeout_at<=? AND w.run_id>?
UNION
SELECT w.run_id FROM pipeline_signal_waits w JOIN pipeline_runs r ON r.id=w.run_id
WHERE r.status='waiting' AND r.current_step_id=w.step_id
  AND w.status IN ('delivered','timed_out') AND w.run_id>?
ORDER BY run_id LIMIT 128`, tsformat.Format(time.Now()), cursor, cursor)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("event wait sweep query failed", "error", err)
				}
				return
			}
			var ids []string
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				logger.Warn("event wait sweep scan failed", "error", err)
				return
			}
			if len(ids) == 0 {
				cursor = ""
				return
			}
			for _, id := range ids {
				if ctx.Err() != nil || (gate != nil && !gate.IsLeader()) {
					return
				}
				mu.Lock()
				if active[id] {
					mu.Unlock()
					cursor = id
					continue
				}
				select {
				case admissions <- struct{}{}:
				default:
					mu.Unlock()
					return // admission full; retry remaining IDs next tick
				}
				active[id] = true
				cursor = id
				mu.Unlock()
				executions.Add(1)
				go func(runID string) {
					defer executions.Done()
					defer func() { mu.Lock(); delete(active, runID); mu.Unlock() }()
					var admitted sync.Once
					releaseAdmission := func() { admitted.Do(func() { <-admissions }) }
					defer releaseAdmission() // every failed/declined admission path
					exec.resumeEventRun(ctx, runID, logger, false, releaseAdmission)
				}(id)
			}
		}
		sweep()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(cancel); <-done }
}

// ResumeEventRun is synchronous so lifecycle owners can account for all work.
// Wait for the original run's registry lifetime before loading its durable
// state: delivery/expiry can race the original MarkWaiting/slot release.
func (e *Executor) ResumeEventRun(ctx context.Context, runID string, logger *slog.Logger) {
	e.resumeEventRun(ctx, runID, logger, true, nil)
}

func (e *Executor) resumeEventRun(ctx context.Context, runID string, logger *slog.Logger, waitForSlot bool, onAdmitted func()) {
	if e.runStore == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	if e.runs != nil {
		if released := e.runs.released(runID); released != nil {
			if !waitForSlot {
				select {
				case <-released:
				default:
					return // original lifetime owns it; retry durably next tick
				}
			} else {
				select {
				case <-released:
				case <-ctx.Done():
					return
				}
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	rec, err := e.runStore.Get(ctx, runID)
	if err != nil {
		logger.Warn("event resume: load run", "run_id", runID, "error", err)
		return
	}
	if rec.Status != RunStatusWaiting {
		return
	}
	plan, reason := e.buildResumePlan(ctx, rec)
	if plan == nil {
		writer, ok := quiesce.Enter(ctx)
		if !ok {
			return
		}
		defer writer.Leave()
		ctx = writer.Context()
		if err := e.runStore.MarkInterrupted(ctx, runID, "not resumable after event: "+reason); err != nil {
			logger.Warn("event resume: interrupt write failed", "run_id", runID, "error", err)
		}
		return
	}
	plan.reason = resumeReasonSignal
	if e.signalWaits != nil {
		status, err := e.signalWaits.Status(ctx, runID, rec.CurrentStepID)
		if err != nil {
			logger.Warn("event resume: read wait", "run_id", runID, "error", err)
			return
		}
		if status == "pending" {
			expired, err := e.signalWaits.Expired(ctx, runID, rec.CurrentStepID)
			if err != nil {
				logger.Warn("event resume: read deadline", "run_id", runID, "error", err)
				return
			}
			if !expired {
				return
			} // spurious/obsolete wake cannot resume a new wait
			plan.reason = resumeReasonEventTimeout
		} else if status == "timed_out" {
			plan.reason = resumeReasonEventTimeout
		}
	}
	e.runResumedRunWithRetry(ctx, plan, logger, waitForSlot, onAdmitted)
}
