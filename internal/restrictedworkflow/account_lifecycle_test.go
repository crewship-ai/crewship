//go:build linux

package restrictedworkflow

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

// Exercises the actual common dispatcher and typed proof runner. The controlled
// session provider counts external starts; it does not prove container isolation.
func TestRestrictedWorkflowAccountLifecycleAtDispatch(t *testing.T) {
	for _, scenario := range []string{"active-null", "active-empty", "suspended-queued", "suspended-recovered", "suspended-between-leaves", "deleted-account"} {
		t.Run(scenario, func(t *testing.T) {
			s, runner := fixture(t)
			receipt := admitFixtureJob(t, s)
			starts := 0
			base := runner.StartSession
			runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
				starts++
				out, err := base(ctx, handle)
				if err == nil && scenario == "suspended-between-leaves" && starts == 1 {
					_, err = s.db.Exec(`UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='h1'`)
				}
				return out, err
			}
			if scenario == "suspended-recovered" {
				claim, err := s.ledger.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "prior-process", Limits: work.SerialAgentLimits(), WorkID: receipt.ID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec(`UPDATE work_attempts SET lease_expires_at='2000-01-01T00:00:00Z' WHERE run_id=?`, claim.RunID); err != nil {
					t.Fatal(err)
				}
				if _, err = s.ledger.RecoverExpiredLeases(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "active-empty":
				if _, err := s.db.Exec(`UPDATE users SET suspended_at='' WHERE id='h1'`); err != nil {
					t.Fatal(err)
				}
			case "suspended-queued", "suspended-recovered":
				if _, err := s.db.Exec(`UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='h1'`); err != nil {
					t.Fatal(err)
				}
			case "deleted-account":
				if _, err := s.db.Exec(`DELETE FROM users WHERE id='h1'`); err != nil {
					t.Fatal(err)
				}
			}
			worked, err := s.DispatchNext(t.Context())
			if scenario == "active-null" || scenario == "active-empty" {
				if !worked || err != nil || starts != 2 {
					t.Fatalf("positive dispatch worked=%t err=%v starts=%d", worked, err, starts)
				}
				result, err := s.Result(t.Context(), "h1", "w", receipt.ID)
				if err != nil || result.State != "completed" || len(result.Outputs) != 2 {
					t.Fatalf("positive result=%+v err=%v", result, err)
				}
			} else {
				wantStarts := 0
				if scenario == "suspended-between-leaves" {
					wantStarts = 1
				}
				if starts != wantStarts {
					t.Fatalf("inactive account reached execution: worked=%t err=%v starts=%d want=%d", worked, err, starts, wantStarts)
				}
				result, resultErr := s.Result(t.Context(), "h1", "w", receipt.ID)
				if resultErr == nil && len(result.Outputs) > 0 {
					t.Fatalf("inactive account received outputs: %+v", result)
				}
			}
		})
	}
}
