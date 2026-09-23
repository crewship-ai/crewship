package api

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestScheduledRuntime_ResolvedCrewCannotChangeAfterAcceptance(t *testing.T) {
	rig := newVerticalRig(t)
	receipt := acceptScheduledInRig(t, rig)
	item, err := rig.store.Get(t.Context(), receipt.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	item.CrewID = "different-crew" // model a crew move after the earlier authorization
	base := NewWebhookRuntime(rig.router.webhookHandler)
	runtime := NewScheduledRuntime(base, nil, 4096, 2)
	err = runtime.Run(t.Context(), dispatch.Assignment{Item: item, RunID: "crew-move-run", Generation: 1}, nil)
	if !errors.Is(err, errWebhookInputUnreadable) {
		t.Fatalf("crew changed after acceptance: %v", err)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("changed crew launched agent: %v", got)
	}
}

func TestVerticalScheduledWork_CronBootSweepThroughSharedDispatcher(t *testing.T) {
	rig := newVerticalRig(t)
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute).Format(time.RFC3339)
	if _, err := rig.db.ExecContext(t.Context(), `UPDATE agents SET schedule_enabled=1,
		schedule_cron='0 8 * * *', schedule_prompt='boot proof', schedule_next_run=? WHERE id=?`,
		due, rig.agentID); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name string
	var dbFile sql.NullString
	if err := rig.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbFile); err != nil || dbFile.String == "" {
		t.Fatalf("database path: %v", err)
	}
	acceptor, err := work.OpenAcceptor(dbFile.String, work.DefaultAcceptanceBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer acceptor.Close()
	guard := work.NewDiskGuard(filepath.Dir(dbFile.String))
	guard.MinFree = 1
	stop, hint, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	sched := scheduler.New(rig.db, nil, nil, nil, nil, nil, scheduler.Config{}, quietLogger())
	if err := sched.SetDurableAcceptance(acceptor, guard, hint); err != nil {
		t.Fatal(err)
	}
	if err := sched.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer sched.Stop()
	deadline := time.Now().Add(5 * time.Second)
	var workID string
	for workID == "" {
		_ = rig.db.QueryRow(`SELECT id FROM work_items WHERE source='schedule' AND agent_id=?`, rig.agentID).Scan(&workID)
		if time.Now().After(deadline) {
			t.Fatal("cron boot sweep did not accept overdue occurrence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rig.waitForState(workID, work.StateSucceeded)
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Fatalf("cron-to-dispatch starts=%v", got)
	}
}

// The HTTP resolver, run record and dispatcher are production code. Only the
// operating-system agent process and container provider are substituted by
// newVerticalRig, as in the webhook vertical tests.
func TestVerticalScheduledWork_OneAcceptedOccurrenceRunsOnce(t *testing.T) {
	rig := newVerticalRig(t)
	receipt := acceptScheduledInRig(t, rig)
	stop, hint, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	hint(receipt.WorkID)
	rig.waitForState(receipt.WorkID, work.StateSucceeded)
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Fatalf("scheduled occurrence started %d agent processes, want one: %v", len(got), got)
	}
	runID := rig.attemptRunID(receipt.WorkID)
	if n := rig.runRecords(runID); n != 1 {
		t.Fatalf("scheduled attempt %s has %d run records, want one", runID, n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rig.count(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.completed'`, runID) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("scheduled run %s was not projected to a completed run record", runID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := rig.store.Get(t.Context(), receipt.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Source != work.SourceSchedule || item.DomainKind != work.DomainAgentRun || item.Attempts != 1 {
		t.Fatalf("settled scheduled item: %+v", item)
	}
}

func acceptScheduledInRig(t *testing.T, rig *verticalRig) work.Receipt {
	t.Helper()
	now := time.Now().UTC()
	due := now.Add(-time.Minute).Truncate(time.Minute).Format(time.RFC3339)
	if _, err := rig.db.ExecContext(t.Context(), `UPDATE agents SET schedule_enabled=1,
		schedule_cron='* * * * *', schedule_prompt='scheduled proof', schedule_next_run=?
		WHERE id=?`, due, rig.agentID); err != nil {
		t.Fatal(err)
	}
	tx, err := rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := scheduler.AcceptDueTx(t.Context(), tx, rig.store, rig.wsID, rig.agentID, now, work.DefaultIngressLimits())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestVerticalScheduledWork_SharesAgentSlotWithWebhook(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	stop, hint, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	webhook := rig.deliver(verticalBody)
	rig.waitForState(webhook.WorkID, work.StateRunning)
	scheduled := acceptScheduledInRig(t, rig)
	hint(scheduled.WorkID)
	// The dispatcher polls while the webhook owns the agent slot. A claim
	// that ignored cross-source capacity would start a second process here.
	time.Sleep(2200 * time.Millisecond)
	item, err := rig.store.Get(t.Context(), scheduled.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateQueued || item.Attempts != 0 {
		t.Fatalf("scheduled work consumed a busy agent slot: %+v", item)
	}
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Fatalf("busy agent launched %d processes: %v", len(got), got)
	}
	close(rig.proc.hold)
	rig.waitForState(webhook.WorkID, work.StateSucceeded)
	rig.waitForState(scheduled.WorkID, work.StateSucceeded)
	if got := rig.proc.runsStarted(); len(got) != 2 {
		t.Fatalf("after capacity freed, starts=%v", got)
	}
}

func TestVerticalScheduledWork_DisabledWhileQueuedNeverStarts(t *testing.T) {
	rig := newVerticalRig(t)
	receipt := acceptScheduledInRig(t, rig)
	if _, err := rig.db.ExecContext(t.Context(), `UPDATE agents SET schedule_enabled=0 WHERE id=?`, rig.agentID); err != nil {
		t.Fatal(err)
	}
	stop, _, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	rig.waitForState(receipt.WorkID, work.StateFailed)
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("revoked schedule ran an agent: %v", got)
	}
}

func TestVerticalScheduledWork_CancelBeforeDispatchNeverStarts(t *testing.T) {
	rig := newVerticalRig(t)
	receipt := acceptScheduledInRig(t, rig)
	if _, err := rig.store.RequestCancel(t.Context(), receipt.WorkID, "operator", "no longer needed"); err != nil {
		t.Fatal(err)
	}
	stop, _, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	rig.waitForState(receipt.WorkID, work.StateCancelled)
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("cancelled schedule ran an agent: %v", got)
	}
}

func TestVerticalScheduledWork_CancelStopsOnlyOwnedRuntime(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	receipt := acceptScheduledInRig(t, rig)
	stop, hint, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	hint(receipt.WorkID)
	rig.waitForState(receipt.WorkID, work.StateRunning)
	runID := rig.attemptRunID(receipt.WorkID)
	if _, err := rig.store.RequestCancel(t.Context(), receipt.WorkID, "operator", "stop scheduled turn"); err != nil {
		t.Fatal(err)
	}
	rig.waitForState(receipt.WorkID, work.StateCancelled)
	if !rig.proc.wasStopped(runID) {
		t.Fatalf("cancel never signalled scheduled runtime %s", runID)
	}
	if got := rig.proc.runsStarted(); len(got) != 1 || got[0] != runID {
		t.Fatalf("cancel crossed run identity: %v, want [%s]", got, runID)
	}
}
