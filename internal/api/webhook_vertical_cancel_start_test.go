package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

// A cancel that lands while the crew container is still being prepared —
// before any agent process exists — is the one case where "cancelled" can be
// stated as a fact rather than requested: nothing was created, and nothing may
// be created afterwards. The barrier here sits INSIDE the container start,
// which is where the previous shape lost the case: the runtime had no launch
// location yet, so every probe answered "unknown", and a cancel during the
// longest part of a cold start ended in reconciliation for work that had never
// reached an agent.
//
// The two halves are separate assertions: the ledger says cancelled, and the
// agent was never started once the preparation completed.

// holdingContainer blocks EnsureCrewRuntime until the run's context is
// cancelled — which is what a Stop before launch does — or until released.
// In the default mode the start then COMPLETES regardless: the point is a
// container start that succeeds after the stop, so the gate, not a failed
// start, is what keeps the agent out. abortOnStop makes it fail instead, the
// shape of a provider that honours its context.
type holdingContainer struct {
	verticalContainer
	entered     chan struct{}
	release     chan struct{}
	abortOnStop bool
}

func (c *holdingContainer) EnsureCrewRuntime(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		if c.abortOnStop {
			return "", ctx.Err()
		}
	}
	return c.verticalContainer.EnsureCrewRuntime(context.Background(), cfg)
}

func (rig *verticalRig) cancelOverHTTP(t *testing.T, workID string) string {
	t.Helper()
	token := rig.sessionToken(t)
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/workspaces/%s/work-items/%s/cancel", rig.baseURL, rig.wsID, workID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Outcome
}

func TestVerticalServer_CancelDuringContainerStartIsConfirmedAndNoAgentStarts(t *testing.T) {
	rig := newVerticalRig(t)
	hold := &holdingContainer{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { close(hold.release) })
	rig.router.webhookHandler.container = hold
	// The production orchestrator's probes over the fake transport, as in the
	// launch-location test: a Stop before launch must be answered by the launch
	// state, not by a fake that says "stopped" for a run it never saw.
	rig.router.webhookHandler.orch = &locatedVerticalRunner{
		Orchestrator: orchestrator.New(probingVerticalContainer{proc: rig.proc}, nil, slog.Default()), proc: rig.proc}
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("deliver = %d", rec.Status)
	}
	select {
	case <-hold.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the crew container start was never entered")
	}

	// The attempt is live (claimed, start intent written) and the container
	// is mid-start. Cancel over the real route: a live attempt can only be
	// ASKED.
	if outcome := rig.cancelOverHTTP(t, rec.WorkID); outcome != string(work.CancelOutcomeRequested) {
		t.Fatalf("cancel outcome = %q, want requested — the attempt must have been live, or this "+
			"test is not exercising the container start", outcome)
	}

	// The dispatcher delivers the stop; the container start then COMPLETES
	// anyway. Nothing may start an agent behind a confirmed cancel.
	it := rig.waitForState(rec.WorkID, work.StateCancelled, work.StateNeedsReconciliation)
	if it.State != work.StateCancelled {
		t.Fatalf("state = %q (%s), want cancelled — no agent existed, so the stop is a fact, not a question",
			it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started after the cancel was confirmed, want 0", len(got))
	}
	if n := rig.allRunRecords(); n != 0 {
		t.Fatalf("%d run records for work that was cancelled before its agent existed, want 0", n)
	}

	// And it stays that way: the dispatcher keeps polling, cancelled is terminal.
	time.Sleep(3 * time.Second)
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started later for cancelled work, want 0", len(got))
	}
}

// A shutdown during the same window is not a cancellation: nothing was
// started, so the work is safe to retry after the restart, and an agent must
// still not start behind the stop.
func TestVerticalServer_ShutdownDuringContainerStartRetriesWithoutAnAgent(t *testing.T) {
	rig := newVerticalRig(t)
	hold := &holdingContainer{entered: make(chan struct{}, 1), release: make(chan struct{}), abortOnStop: true}
	t.Cleanup(func() { close(hold.release) })
	rig.router.webhookHandler.container = hold
	// The production orchestrator's probes over the fake transport, as in the
	// launch-location test: a Stop before launch must be answered by the launch
	// state, not by a fake that says "stopped" for a run it never saw.
	rig.router.webhookHandler.orch = &locatedVerticalRunner{
		Orchestrator: orchestrator.New(probingVerticalContainer{proc: rig.proc}, nil, slog.Default()), proc: rig.proc}
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("deliver = %d", rec.Status)
	}
	select {
	case <-hold.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the crew container start was never entered")
	}

	rig.stopDispatcher()
	rig.stopDispatcher = nil

	it, err := rig.store.Get(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != work.StateRetryWait && it.State != work.StateQueued {
		t.Fatalf("state after shutdown = %q (%s), want retry_wait/queued — nothing had started, and a "+
			"shutdown is not a user cancellation", it.State, it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Fatalf("%d agent runtimes started across a shutdown, want 0", len(got))
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_events WHERE work_id = ? AND to_state = 'cancelled'`, rec.WorkID); n != 0 {
		t.Fatalf("shutdown recorded %d cancelled transitions", n)
	}
	if it.Generation != 1 {
		t.Fatalf("generation = %d, want the single attempt's", it.Generation)
	}
}
