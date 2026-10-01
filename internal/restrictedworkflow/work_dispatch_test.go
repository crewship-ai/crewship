//go:build linux

package restrictedworkflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func admitFixtureJob(t *testing.T, s *Service) Receipt {
	t.Helper()
	r, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "PRIVATE_LEDGER_CANARY"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestWorkflowLedgerAtomicRollbackAndContentFreeIdentity(t *testing.T) {
	s, _ := fixture(t)
	r := admitFixtureJob(t, s)
	var input, source, author, revision string
	if err := s.db.QueryRow(`SELECT input_json,source_ref,authorized_by_user_id,target_revision FROM work_items WHERE id=?`, r.ID).Scan(&input, &source, &author, &revision); err != nil {
		t.Fatal(err)
	}
	if input != "{}" || source != "" || author != "" || revision != "" {
		t.Fatalf("private payload entered shared ledger: %q %q %q %q", input, source, author, revision)
	}
	p, err := s.PrepareManualWithRights(t.Context(), "h1", "w", "private-work", map[string]any{"task": "ROLLBACK"}, "", []access.Right{{Kind: "project", ID: "missing", Operation: "read"}}, "issue")
	if p != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid preparation %v", err)
	}
	// Force failure AFTER the private job is inserted, before common acceptance.
	if _, err = s.db.Exec(`CREATE TRIGGER reject_common_work BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'test acceptance failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "ROLLBACK"}, "", 0); err == nil {
		t.Fatal("accepted after failed ledger insertion")
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM restricted_workflow_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("domain survived rollback: %d %v", count, err)
	}
}
func TestWorkflowSharesAgentCapacityAndDoesNotClaimForeignKinds(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.ledger.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: "w", Source: work.SourceSchedule, DomainKind: work.DomainAgentRun, AgentID: "agent", Class: work.ClassBackground})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ledger.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "trusted", Limits: work.SerialAgentLimits(), WorkID: other.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		starts++
		return base(ctx, h)
	}
	if worked, err := s.DispatchNext(t.Context()); worked || err != nil || starts != 0 {
		t.Fatalf("shared capacity bypass %v %v %d", worked, err, starts)
	}
	if err = s.ledger.Transition(t.Context(), work.TransitionRequest{WorkID: other.WorkID, RunID: claimed.RunID, Generation: claimed.Generation, To: work.StateFailed, Reason: "synthetic trusted attempt ended"}); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil || starts != 2 {
		t.Fatalf("private dispatch %v %v %d", worked, err, starts)
	}
	result, err := s.Result(t.Context(), "h1", "w", r.ID)
	if err != nil || result.State != "completed" {
		t.Fatalf("result %+v %v", result, err)
	}
}
func TestWorkflowStaleGenerationFencesNextLeafAndLateCompletion(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	var handle string
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		starts++
		handle = h
		// Supersede while the first model process is returning. There must be no
		// second model call and no write from the stale graph into domain results.
		if _, err := s.db.Exec(`UPDATE work_items SET generation=generation+1 WHERE id=?`, r.ID); err != nil {
			t.Fatal(err)
		}
		return base(ctx, h)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil || starts != 1 {
		t.Fatalf("stale graph continued: %v %v %d", worked, err, starts)
	}
	if _, err := runner.Authority.Store.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("stale descendant credential authority survived: %v", err)
	}
	var state, outputs string
	if err := s.db.QueryRow(`SELECT state,outputs_json FROM restricted_workflow_jobs WHERE id=?`, r.ID).Scan(&state, &outputs); err != nil || state != "running" || outputs != "{}" {
		t.Fatalf("stale completion wrote domain result: %s %s %v", state, outputs, err)
	}
}
func TestWorkflowPlannedLeaseRecoversButStartIntentParks(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "start intended"}[started], func(t *testing.T) {
			s, _ := fixture(t)
			r := admitFixtureJob(t, s)
			c, err := s.ledger.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "old", Limits: work.SerialAgentLimits(), WorkID: r.ID})
			if err != nil {
				t.Fatal(err)
			}
			if started {
				if err = s.ledger.MarkStarting(t.Context(), r.ID, c.RunID, c.Generation, "private-locator"); err != nil {
					t.Fatal(err)
				}
			}
			// A restart BEFORE expiry must not revoke work owned by a live lease.
			if _, err = s.ledger.RecoverExpiredLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			it, err := s.ledger.Get(t.Context(), r.ID)
			if err != nil || it.State != work.StateStarting {
				t.Fatalf("unexpired lease lost: %+v %v", it, err)
			}
			if _, err = s.db.Exec(`UPDATE work_attempts SET lease_expires_at='2000-01-01T00:00:00Z' WHERE run_id=?`, c.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ledger.RecoverExpiredLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			worked, err := s.DispatchNext(t.Context())
			if started {
				if worked || err != nil {
					t.Fatalf("unknown effects replayed %v %v", worked, err)
				}
				result, e := s.Result(t.Context(), "h1", "w", r.ID)
				if e != nil || result.State != "needs_reconciliation" || len(result.Outputs) != 0 {
					t.Fatalf("recovery receipt %+v %v", result, e)
				}
			} else if !worked || err != nil {
				t.Fatalf("lost planned work %v %v", worked, err)
			}
		})
	}
}
func TestWorkflowLostWakeRecoveredAtStartup(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	recovered, err := New(s.db, runner)
	if err != nil {
		t.Fatal(err)
	}
	// The new dispatcher received none of the old process's in-memory hints.
	if err = recovered.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	// This is a bounded liveness check, not a latency benchmark. Race builds
	// share the host with the full backend suite and expensive migrations.
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		result, err := recovered.Result(t.Context(), "h1", "w", r.ID)
		if err == nil && result.State == "completed" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("lost wake stranded durable work: %+v %v", result, err)
		case <-tick.C:
		}
	}
}
func TestWorkflowMigrationParksLegacyStartedWorkAndPreservesData(t *testing.T) {
	s, _ := fixture(t)
	first := admitFixtureJob(t, s)
	second := admitFixtureJob(t, s)
	if _, err := s.db.Exec(`UPDATE restricted_workflow_jobs SET state='running' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM work_items; DROP INDEX work_restricted_domain; DROP TABLE restricted_workflow_attempt_roots; DROP TRIGGER restricted_workflow_descendant_binding`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261001160148_restricted_work_ownership.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, state string }{{first.ID, "queued"}, {second.ID, "needs_reconciliation"}} {
		var state, inputs string
		if err = s.db.QueryRow(`SELECT w.state,j.inputs_json FROM work_items w JOIN restricted_workflow_jobs j ON j.id=w.id WHERE w.id=?`, row.id).Scan(&state, &inputs); err != nil || state != row.state || !strings.Contains(inputs, "PRIVATE_LEDGER_CANARY") {
			t.Fatalf("migration lost intent/data %s %s %v", state, inputs, err)
		}
	}
}

type unconfirmedGraphStop struct{ *restricteddispatch.TextRunner }

func (unconfirmedGraphStop) ConfirmWorkflowStopped(context.Context, string) error {
	return errors.New("synthetic daemon unavailable")
}
func TestWorkflowUnconfirmedContainerStopWithholdsCapturedOutput(t *testing.T) {
	s, runner := fixture(t)
	s.executor = unconfirmedGraphStop{runner}
	r := admitFixtureJob(t, s)
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
		t.Fatalf("unconfirmed stop reported success %v %v", worked, err)
	}
	result, err := s.Result(t.Context(), "h1", "w", r.ID)
	if err != nil || result.State != "needs_reconciliation" || len(result.Outputs) != 0 {
		t.Fatalf("uncertain output escaped: %+v %v", result, err)
	}
	it, err := s.ledger.Get(t.Context(), r.ID)
	if err != nil || it.State != work.StateNeedsReconciliation {
		t.Fatalf("capacity freed before stop: %+v %v", it, err)
	}
	// An investigated resolution can acknowledge the already captured result.
	if err = s.ledger.Resolve(t.Context(), r.ID, it.Generation, work.StateSucceeded, "owner", "synthetic independent stop confirmation"); err != nil {
		t.Fatal(err)
	}
	result, err = s.Result(t.Context(), "h1", "w", r.ID)
	if err != nil || result.State != "completed" || len(result.Outputs) != 2 {
		t.Fatalf("captured result lost after resolution: %+v %v", result, err)
	}
}

func TestWorkflowNegativeResolutionWithholdsCapturedOutputAndStopsPolling(t *testing.T) {
	for _, state := range []work.State{work.StateFailed, work.StateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			s, runner := fixture(t)
			s.executor = unconfirmedGraphStop{runner}
			r := admitFixtureJob(t, s)
			if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
				t.Fatalf("unconfirmed stop reported success %v %v", worked, err)
			}
			it, err := s.ledger.Get(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ledger.Resolve(t.Context(), r.ID, it.Generation, state, "owner", "investigated outcome rejected"); err != nil {
				t.Fatal(err)
			}
			result, err := s.Result(t.Context(), "h1", "w", r.ID)
			want := "failed"
			if state == work.StateCancelled {
				want = "canceled"
			}
			if err != nil || result.State != want || len(result.Outputs) != 0 {
				t.Fatalf("negative resolution must be terminal and private: %+v %v", result, err)
			}
		})
	}
}

func TestWorkflowExpiredLeaseFencesCredentialResolutionAndNextLeaf(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		starts++
		if _, err := s.db.Exec(`UPDATE work_attempts SET lease_expires_at='2000-01-01T00:00:00Z' WHERE work_id=?`, r.ID); err != nil {
			t.Fatal(err)
		}
		return base(ctx, h)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil || starts != 1 {
		t.Fatalf("expired lease continued: %v %v %d", worked, err, starts)
	}
	it, err := s.ledger.Get(t.Context(), r.ID)
	if err != nil || it.State != work.StateNeedsReconciliation {
		t.Fatalf("unknown effects retried: %+v %v", it, err)
	}
}

func TestWorkflowMemberRemovalRetainsStopIdentitiesAndRevokesDescendants(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	var handle string
	starts := 0
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		starts++
		handle = h
		if _, err := s.db.Exec(`DELETE FROM workspace_members WHERE id='m1'`); err != nil {
			t.Fatal(err)
		}
		return nil, access.ErrDenied
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil || starts != 1 {
		t.Fatalf("removed member execution %v %v %d", worked, err, starts)
	}
	if _, err := runner.Authority.Store.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("removed actor retained authority %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM restricted_workflow_attempt_roots WHERE workflow_id=?`, r.ID).Scan(&count); err != nil || count < 2 {
		t.Fatalf("lost graph/descendant stop identities %d %v", count, err)
	}
	if _, err := s.Result(t.Context(), "h1", "w", r.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed member receipt survived %v", err)
	}
}

func TestWorkflowInterruptedExecutionKeepsAuthorizedReconciliationReceipt(t *testing.T) {
	s, runner := fixture(t)
	r := admitFixtureJob(t, s)
	runner.StartSession = func(context.Context, string) (restricteddispatch.TextSession, error) { return nil, context.Canceled }
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
		t.Fatalf("interrupted work %v %v", worked, err)
	}
	result, err := s.Result(t.Context(), "h1", "w", r.ID)
	if err != nil || result.State != "needs_reconciliation" || len(result.Outputs) != 0 {
		t.Fatalf("interrupted receipt lost or released output: %+v %v", result, err)
	}
	if worked, err := s.DispatchNext(t.Context()); worked || err != nil {
		t.Fatalf("interrupted model request repeated %v %v", worked, err)
	}
}
