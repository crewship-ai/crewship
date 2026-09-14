package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

// The creation boundary, tested where the reviews found the gaps.
//
// Two independent reviews reproduced the same hole from two sides. First: the
// launch location was recorded before the process existed, a stop's kill
// probe answered ABSENT because there was nothing to kill yet, and the
// preflight then created the agent behind a confirmed cancellation. Second:
// the fix leaned on the exec.command journal row to tell "never requested"
// from "requested and gone", but that row is queued telemetry whose
// persistence and failure are both invisible — so its absence proved nothing.
//
// The protocol now: the orchestrator asks AgentRunRequest.ExecGate
// synchronously, immediately before it creates the exec. A stop recorded
// before that moment refuses the creation, so "stopped" is a fact by
// construction; a creation admitted there is recorded durably on the attempt
// (runtime phase `requested`) before it happens, and from then on nothing
// observed at the location proves the process did not run. The journal is
// never consulted. These tests drive that protocol through the production
// orchestrator's probes, the real dispatcher, the real HTTP cancel route and
// real SQLite, with the agent process and the container transport
// substituted.

// preflightRunner replaces RunAgent with the production order of events made
// observable: enter (location already recorded) → hold → ask the creation
// gate → create. `holdAfterGate` moves the hold to just after the gate, which
// is the "creation admitted, process not yet confirmed" window. honourCtx
// models a preflight that respects its context (and, after the gate, a
// creation that fails); without it the preflight completes regardless and
// only the gate can refuse. journal, when set, is
// what the runner emits exec.command through once the gate admits — held or
// failing, to model the telemetry the previous fix trusted.
type preflightRunner struct {
	*orchestrator.Orchestrator
	proc          *fakeAgentProcess
	entered       chan struct{}
	enterOnce     sync.Once
	release       chan struct{}
	releaseOnce   sync.Once
	honourCtx     bool
	holdAfterGate bool
	journal       *journal.Writer
	gateResults   chan error
}

func (r *preflightRunner) letGo() { r.releaseOnce.Do(func() { close(r.release) }) }

func (r *preflightRunner) hold(ctx context.Context) {
	select {
	case <-r.release:
	case <-ctx.Done():
	}
}

func (r *preflightRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, h orchestrator.EventHandler) error {
	if !r.holdAfterGate {
		r.enterOnce.Do(func() { close(r.entered) })
		r.hold(ctx)
		if r.honourCtx && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// The gate, asked exactly where the production orchestrator asks it.
	if req.ExecGate != nil {
		err := req.ExecGate(ctx)
		if r.gateResults != nil {
			r.gateResults <- err
		}
		if err != nil {
			return fmt.Errorf("%w: %w", orchestrator.ErrExecRefused, err)
		}
	}
	if r.journal != nil {
		// The telemetry the previous fix trusted, emitted the way the
		// orchestrator emits it: queued, result ignored.
		_, _ = r.journal.Emit(ctx, journal.Entry{WorkspaceID: req.WorkspaceID, TraceID: req.RunID,
			Type: journal.EntryExecCommand, ActorType: journal.ActorAgent, Summary: "exec creation requested"})
	}
	if r.holdAfterGate {
		r.enterOnce.Do(func() { close(r.entered) })
		r.hold(ctx)
		if r.honourCtx {
			// Creation admitted, then the exec create failed — or the process
			// ran briefly and is gone. The orchestrator returns an error either
			// way and cannot tell the two apart; neither may anything above it.
			return errors.New("exec create failed after the request was admitted")
		}
	}
	// The gate was asked above, once, where the orchestrator asks it; the
	// fake process must not ask it a second time.
	req.ExecGate = nil
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

func attemptPhase(t *testing.T, rig *verticalRig, workID string) string {
	t.Helper()
	var phase string
	if err := rig.db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE work_id = ? ORDER BY attempt DESC LIMIT 1`, workID).Scan(&phase); err != nil {
		t.Fatalf("read the attempt phase: %v", err)
	}
	return phase
}

// A cancel before the gate: the stop is recorded, the preflight completes
// regardless (it ignores its context), and the gate refuses the creation.
// No process, no run, `cancelled` — and the attempt never left phase
// `starting`, so the ledger agrees that nothing was ever requested.
func TestVerticalServer_CancelBeforeTheGateRefusesTheCreation(t *testing.T) {
	rig, runner, _ := newPreflightRig(t, false)
	runner.gateResults = make(chan error, 1)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")
	if got := rig.cancelOverHTTP(t, rec.WorkID); got != string(work.CancelOutcomeRequested) {
		t.Fatalf("cancel outcome = %q, want requested", got)
	}
	// The stop is a fact from the moment it is recorded; nothing waits on a
	// probe. Its delivery cancels the run's context, which is what wakes this
	// preflight (it does not honour the context, so it proceeds to the gate
	// rather than aborting) — the deterministic "creation completes after
	// the stop" shape, with no release from the test.
	select {
	case err := <-runner.gateResults:
		if !errors.Is(err, errWebhookStoppedBeforeAgent) {
			t.Fatalf("gate answered %v, want a refusal because a stop was recorded", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the gate was never asked")
	}
	it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation, work.StateSucceeded)
	if it.State != work.StateCancelled {
		t.Fatalf("state = %s (%s), want cancelled", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes created behind a refused gate", len(got))
	}
	if phase := attemptPhase(t, rig, rec.WorkID); phase != "starting" {
		t.Fatalf("attempt phase = %q, want starting — no creation was ever requested", phase)
	}
	time.Sleep(3 * time.Second)
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started later for cancelled work", len(got))
	}
}

// Shutdown before the gate: nothing was requested, the gate refuses, and the
// work is safe to retry after the restart — never cancelled, never parked
// on a guess.
func TestVerticalServer_ShutdownBeforeTheGateRetriesWithoutAnAgent(t *testing.T) {
	rig, runner, _ := newPreflightRig(t, false)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")
	// The stop's delivery cancels the run's context; this preflight ignores
	// it and proceeds to the gate, which refuses. No release from the test —
	// releasing here would race the stop and make the outcome depend on
	// which arrived first.
	rig.stopDispatcher()
	rig.stopDispatcher = nil

	it, err := rig.store.Get(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != work.StateRetryWait {
		t.Fatalf("state after shutdown = %s (%s), want retry_wait — the gate refused, so nothing was created", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes created across the shutdown", len(got))
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
		t.Fatalf("shutdown recorded %d cancelled transitions", n)
	}
}

// The gate admitted the creation and the process is not yet confirmed. What
// the journal did with the exec.command entry — queued, flushed, or failed —
// must make no difference: the outcome is unknown, and unknown holds its
// capacity in reconciliation. Both a user cancel and a shutdown.
func TestVerticalServer_AdmittedCreationIsUnknownWhateverTheJournalDid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		journal func(t *testing.T, rig *verticalRig) *journal.Writer
	}{
		{"exec.command still queued", func(t *testing.T, rig *verticalRig) *journal.Writer {
			w := journal.NewWriter(rig.db, slog.Default(), journal.WriterOptions{FlushSize: 1000, FlushInterval: time.Hour})
			t.Cleanup(func() { _ = w.Close() })
			return w
		}},
		{"exec.command emission fails", func(t *testing.T, rig *verticalRig) *journal.Writer {
			w := journal.NewWriter(rig.db, slog.Default(), journal.WriterOptions{FlushSize: 1})
			_ = w.Close() // every Emit from here fails; the orchestrator ignores that
			return w
		}},
	} {
		t.Run("cancel/"+tc.name, func(t *testing.T) {
			rig, runner, container := newPreflightRig(t, true)
			runner.holdAfterGate = true
			runner.journal = tc.journal(t, rig)
			rig.startDispatcher()

			rec := rig.deliver(verticalBody)
			awaitSignal(t, runner.entered, "the admitted creation")
			if phase := attemptPhase(t, rig, rec.WorkID); phase != "requested" {
				t.Fatalf("attempt phase = %q after the gate admitted, want requested", phase)
			}
			if got := rig.cancelOverHTTP(t, rec.WorkID); got != string(work.CancelOutcomeRequested) {
				t.Fatalf("cancel outcome = %q, want requested", got)
			}
			awaitSignal(t, container.killed, "the stop probe")
			if it, _ := rig.store.Get(context.Background(), rec.WorkID); it.State.Terminal() {
				t.Fatalf("an absent probe after an admitted creation settled the work as %s", it.State)
			}
			// The creation fails on its cancelled context — or ran briefly; the
			// orchestrator cannot tell, and neither may we.
			runner.letGo()
			it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation, work.StateSucceeded)
			if it.State != work.StateNeedsReconciliation {
				t.Fatalf("state = %s (%s), want needs_reconciliation — a requested, unconfirmed, absent process is unknown", it.State, it.StateReason)
			}
			if !it.State.HoldsExecutionSlot() {
				t.Fatal("an unknown outcome released its execution slot")
			}
		})
		t.Run("shutdown/"+tc.name, func(t *testing.T) {
			rig, runner, _ := newPreflightRig(t, true)
			runner.holdAfterGate = true
			runner.journal = tc.journal(t, rig)
			rig.startDispatcher()

			rec := rig.deliver(verticalBody)
			awaitSignal(t, runner.entered, "the admitted creation")
			rig.stopDispatcher()
			rig.stopDispatcher = nil
			it, err := rig.store.Get(context.Background(), rec.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if it.State != work.StateNeedsReconciliation {
				t.Fatalf("state after shutdown = %s (%s), want needs_reconciliation", it.State, it.StateReason)
			}
			if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
				t.Fatalf("shutdown recorded %d cancelled transitions", n)
			}
		})
	}
}

// The gate admitted the creation, the stop arrives, the process is created
// anyway (the creation ignores its context) and runs to completion. The
// truth is `succeeded`; a late cancel changes nothing about what happened.
func TestVerticalServer_CompletionAfterAnAdmittedCreationIsNotCalledCancelled(t *testing.T) {
	rig, runner, container := newPreflightRig(t, false)
	runner.holdAfterGate = true
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the admitted creation")
	if got := rig.cancelOverHTTP(t, rec.WorkID); got != string(work.CancelOutcomeRequested) {
		t.Fatalf("cancel outcome = %q, want requested", got)
	}
	awaitSignal(t, container.killed, "the stop probe")

	runner.letGo() // the process comes to be and finishes its turn
	it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation, work.StateSucceeded)
	if it.State != work.StateSucceeded {
		t.Fatalf("state = %s (%s), want succeeded — the agent ran to completion", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Fatalf("%d runtimes, want the one that completed", len(got))
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
		t.Fatalf("%d cancelled transitions recorded over a completed run", n)
	}
}

// The gate is the last word before creation, and a gate whose durable write
// fails refuses: the creation does not happen, and the attempt is retryable.
func TestVerticalServer_AGateThatCannotRecordRefusesTheCreation(t *testing.T) {
	rig, runner, _ := newPreflightRig(t, false)
	runner.gateResults = make(chan error, 1)
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	awaitSignal(t, runner.entered, "the launch preflight")
	// Close the attempt underneath the gate: MarkRuntimeRequested must then
	// refuse (the binding no longer describes a live starting attempt).
	if _, err := rig.db.Exec(`UPDATE work_attempts SET runtime_phase = 'planned' WHERE work_id = ?`, rec.WorkID); err != nil {
		t.Fatal(err)
	}
	runner.letGo()
	select {
	case err := <-runner.gateResults:
		if err == nil || !errors.Is(err, errWebhookBeforeAgent) {
			t.Fatalf("gate answered %v, want a before-agent refusal because the request could not be recorded", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the gate was never asked")
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes created after the gate could not record the request", len(got))
	}
	rig.waitForState(rec.WorkID, work.StateRetryWait, work.StateQueued, work.StateNeedsReconciliation)
}
