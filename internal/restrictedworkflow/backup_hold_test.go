//go:build linux

package restrictedworkflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestPrivateWorkflowRespectsBackupAndRecoveryHolds(t *testing.T) {
	for _, kind := range []string{"backup", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			s, runner := fixture(t)
			r, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "held workflow"}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			base := runner.StartSession
			runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
				starts++
				return base(ctx, handle)
			}
			var release func()
			if kind == "backup" {
				w, err := quiesce.Default().Begin(t.Context(), quiesce.Options{HoldCap: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				release = func() { w.Release() }
			} else {
				previous := quiesce.DefaultHolds().List()
				quiesce.DefaultHolds().Replace([]quiesce.Hold{{Key: quiesce.HoldQueue}})
				release = func() { quiesce.DefaultHolds().Replace(previous) }
			}
			t.Cleanup(release)
			worked, err := s.DispatchNext(t.Context())
			if worked || err != nil || starts != 0 {
				t.Fatalf("dispatch under %s hold: worked=%v error=%v starts=%d", kind, worked, err, starts)
			}
			var state string
			if err := s.db.QueryRow(`SELECT state FROM restricted_workflow_jobs WHERE id=?`, r.ID).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("held job must stay pending: state=%s error=%v", state, err)
			}
			release()
			if worked, err := s.DispatchNext(t.Context()); !worked || err != nil || starts != 2 {
				t.Fatalf("dispatch after release: worked=%v error=%v starts=%d", worked, err, starts)
			}
		})
	}
}

// Open a window after the common dispatcher claims work, before runtime admission.
// The second attempt uses the same runtime without opening another window.
type quiesceBeforeWorkflowRun struct {
	*workflowRuntime
	once sync.Once
}

func (r *quiesceBeforeWorkflowRun) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	var window *quiesce.Window
	var err error
	r.once.Do(func() {
		window, err = quiesce.Default().Begin(ctx, quiesce.Options{HoldCap: time.Minute})
	})
	if err != nil {
		return err
	}
	if window != nil {
		defer window.Release()
	}
	return r.workflowRuntime.Run(ctx, a, started)
}

func TestPrivateWorkflowQuiesceAfterClaimPreservesAuthorityAndRetries(t *testing.T) {
	s, runner := fixture(t)
	receipt := admitFixtureJob(t, s)
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		starts++
		return base(ctx, handle)
	}
	runtime := &quiesceBeforeWorkflowRun{workflowRuntime: s.runtime}
	s.dispatcher = dispatch.New(s.ledger, runtime, runtime, workflowDispatchConfig(), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if worked, err := s.DispatchNext(ctx); !worked || err == nil || starts != 0 {
		t.Fatalf("pre-execution refusal: worked=%v error=%v starts=%d", worked, err, starts)
	}
	item, err := s.ledger.Get(ctx, receipt.ID)
	if err != nil || item.State != work.StateRetryWait {
		t.Fatalf("unstarted workflow lost instead of deferred: %+v %v", item, err)
	}
	result, err := s.Result(ctx, "h1", "w", receipt.ID)
	if err != nil || result.State != "pending" || len(result.Outputs) != 0 {
		t.Fatalf("quiesce refusal revoked private authority: %+v %v", result, err)
	}
	// Advance eligibility without sleeping through the production retry backoff.
	if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET eligible_at='2000-01-01T00:00:00Z' WHERE id=?`, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.DispatchNext(ctx); !worked || err != nil || starts != 2 {
		t.Fatalf("dispatch after release: worked=%v error=%v starts=%d", worked, err, starts)
	}
	result, err = s.Result(ctx, "h1", "w", receipt.ID)
	if err != nil || result.State != "completed" || len(result.Outputs) != 2 {
		t.Fatalf("deferred private workflow did not complete: %+v %v", result, err)
	}
}

func TestPrivateWorkflowStartsBehindBackupWindowAndResumes(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	window, err := quiesce.Default().Begin(t.Context(), quiesce.Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { window.Release() })
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("quiet window must defer recovery, not refuse service startup: %v", err)
	}
	defer s.Close()
	item, err := s.ledger.Get(t.Context(), receipt.ID)
	if err != nil || item.State != work.StateQueued || item.Attempts != 0 || item.Generation != 0 {
		t.Fatalf("startup claimed work behind quiet window: %+v %v", item, err)
	}
	window.Release()
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		result, err := s.Result(t.Context(), "h1", "w", receipt.ID)
		if err == nil && result.State == "completed" && len(result.Outputs) == 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("startup deferred work did not resume after release: %+v %v", result, err)
		case <-tick.C:
		}
	}
}

func TestPrivateWorkflowStartupRecoveryErrorReleasesWriter(t *testing.T) {
	s, _ := fixture(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err == nil || errors.Is(err, ErrDenied) {
		t.Fatalf("startup must propagate the recovery error: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	window, err := quiesce.Default().Begin(ctx, quiesce.Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatalf("startup error leaked its writer: %v", err)
	}
	window.Release()
}
