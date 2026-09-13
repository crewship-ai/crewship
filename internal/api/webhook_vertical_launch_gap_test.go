package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

// The window the second review reproduced: the launch location is recorded
// before the process exists, RunAgent is still in its preflight, and a stop's
// kill probe answers ABSENT because there is nothing to kill YET. The first
// version of this fix trusted that answer as a confirmed stop, the cancel
// watcher went away, and the preflight then created the process behind a
// cancellation the ledger had already confirmed.
//
// The rule now: while creation is pending, absent proves nothing. A stop in
// that phase cancels the creation's context and is answered "not confirmed";
// the ledger settles only when the launch does — a process confirmed present,
// or Run returned so nothing can be created any more — and after Run returns
// without a confirmation, "absent" is a no only if the orchestrator's journal
// shows no exec was ever requested.

// preflightRunner is the production orchestrator's probes with RunAgent
// replaced by a barrier INSIDE the launch: the location is already recorded
// (Launch ran), the process does not exist yet. honourCtx models a creation
// that respects its context (Docker's exec create does); without it the
// creation completes regardless, which is the shape that must never be
// reported as cancelled.
type preflightRunner struct {
	*orchestrator.Orchestrator
	proc        *fakeAgentProcess
	entered     chan struct{}
	enterOnce   sync.Once
	release     chan struct{}
	releaseOnce sync.Once
	honourCtx   bool
}

func (r *preflightRunner) letGo() { r.releaseOnce.Do(func() { close(r.release) }) }

func (r *preflightRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, h orchestrator.EventHandler) error {
	r.enterOnce.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	if r.honourCtx && ctx.Err() != nil {
		// Creation refused: the exec is never created, as Docker refuses a
		// request whose context is already gone.
		return ctx.Err()
	}
	return r.proc.RunAgent(context.Background(), req, h)
}

// absentContainer answers every probe ABSENT — there is no process before
// creation, and the fake agent is not a tmux session afterwards either — and
// reports each kill probe so a test can wait for the stop to have been sent.
type absentContainer struct {
	verticalContainer
	killed chan struct{}
}

func (c absentContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if strings.Contains(strings.Join(cfg.Cmd, " "), "kill-session") {
		select {
		case c.killed <- struct{}{}:
		default:
		}
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT\n"))}, nil
}

func newPreflightRig(t *testing.T, honourCtx bool) (*verticalRig, *preflightRunner, absentContainer) {
	t.Helper()
	rig := newVerticalRig(t)
	container := absentContainer{killed: make(chan struct{}, 1)}
	runner := &preflightRunner{
		Orchestrator: orchestrator.New(container, nil, slog.Default()),
		proc:         rig.proc, entered: make(chan struct{}), release: make(chan struct{}), honourCtx: honourCtx,
	}
	t.Cleanup(runner.letGo)
	rig.router.webhookHandler.orch = runner
	return rig, runner, container
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never happened", what)
	}
}

// 1 + 2. Cancel while creation is pending, creation honours its context: the
// ledger says cancelled and the agent never existed — not even after the
// preflight is released.
func TestVerticalServer_CancelWhileCreationIsPendingNeverLaunches(t *testing.T) {
	rig, runner, container := newPreflightRig(t, true)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")
	if got := rig.cancelOverHTTP(t, rec.WorkID); got != string(work.CancelOutcomeRequested) {
		t.Fatalf("cancel outcome = %q, want requested", got)
	}
	// The stop was delivered: the kill probe went to the container and, with
	// nothing to kill, answered ABSENT. That answer must not settle anything.
	awaitSignal(t, container.killed, "the stop probe")
	if it, err := rig.store.Get(context.Background(), rec.WorkID); err != nil || it.State.Terminal() {
		t.Fatalf("an absent probe during a pending creation settled the work as %v (%v)", it, err)
	}

	// The creation now proceeds — and refuses, because its context is gone.
	runner.letGo()
	it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation, work.StateSucceeded)
	if it.State != work.StateCancelled {
		t.Fatalf("state = %s (%s), want cancelled — nothing was created and the journal shows no exec", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("agent executed AFTER the stop probe answered absent; %d runtimes", len(got))
	}
	time.Sleep(3 * time.Second)
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started later for cancelled work, want 0", len(got))
	}
}

// 3. Shutdown at the same boundary. The stop cannot be confirmed while the
// creation is pending, so the work is parked — never called cancelled, never
// retried on a guess — and the agent still does not start.
func TestVerticalServer_ShutdownWhileCreationIsPendingIsReconciliationNotCancel(t *testing.T) {
	rig, runner, _ := newPreflightRig(t, true)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")

	rig.stopDispatcher()
	rig.stopDispatcher = nil

	it, err := rig.store.Get(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != work.StateNeedsReconciliation {
		t.Fatalf("state after shutdown = %s (%s), want needs_reconciliation — a pending creation is an unclear "+
			"outcome, not a cancellation and not a retry", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started across the shutdown", len(got))
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
		t.Fatalf("shutdown recorded %d cancelled transitions", n)
	}
}

// 5. A creation that ignores the stop and runs to completion is a completed
// turn. The stop probe answered absent while the process did not exist yet,
// the cancel arrived, and the agent ran anyway: the truth is `succeeded`, and
// recording `cancelled` over it would hide the effects.
func TestVerticalServer_CompletionThatBeatsACancelIsNotCalledCancelled(t *testing.T) {
	rig, runner, container := newPreflightRig(t, false)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")
	if got := rig.cancelOverHTTP(t, rec.WorkID); got != string(work.CancelOutcomeRequested) {
		t.Fatalf("cancel outcome = %q, want requested", got)
	}
	awaitSignal(t, container.killed, "the stop probe")

	runner.letGo() // the creation completes and the agent finishes its turn
	it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation, work.StateSucceeded)
	if it.State != work.StateSucceeded {
		t.Fatalf("state = %s (%s), want succeeded — the agent ran to completion; a late cancel changes nothing "+
			"about what happened", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Fatalf("%d runtimes, want the one that completed", len(got))
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
		t.Fatalf("%d cancelled transitions recorded over a completed run", n)
	}
}
