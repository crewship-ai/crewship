//go:build linux

package restrictedworkflow

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
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
