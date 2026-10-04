//go:build linux

package restrictedworkflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestWorkflowAdmissionStorageFailureNeverQueuesPartialJob(t *testing.T) {
	for _, table := range []string{"chats", "access_context", "restricted_workflow_jobs", "restricted_workflow_recipe_bindings", "restricted_workflow_provider_policies"} {
		t.Run(table, func(t *testing.T) {
			s, _ := fixture(t)
			if _, err := s.db.ExecContext(t.Context(), "CREATE TRIGGER fixture_reject BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'fixture admission failure'); END"); err != nil {
				t.Fatal(err)
			}
			receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0)
			if err == nil || receipt.ID != "" {
				t.Fatalf("partial admission reported success: %+v %v", receipt, err)
			}
			for _, check := range []string{"restricted_workflow_jobs", "restricted_workflow_recipe_bindings", "restricted_workflow_provider_policies", "work_items"} {
				var count int
				if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+check).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial %s survived rollback: %d %v", check, count, err)
				}
			}
			var executable int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL AND completed_at IS NULL`).Scan(&executable); err != nil || executable != 0 {
				t.Fatalf("failed admission kept authority: %d %v", executable, err)
			}
			if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER fixture_reject"); err != nil {
				t.Fatal(err)
			}
			receipt = admitFixtureJob(t, s)
			if receipt.ID == "" {
				t.Fatal("retry did not recover")
			}
		})
	}
}

func TestWorkflowAdmissionRefusalsDoNotCreateAuthority(t *testing.T) {
	s, _ := fixture(t)
	for _, tc := range []struct {
		user, workspace, slug, hash string
		inputs                      map[string]any
	}{
		{"missing", "w", "private-work", "", nil},
		{"owner", "w", "private-work", "", nil},
		{"h1", "missing", "private-work", "", nil},
		{"h1", "w", "missing", "", nil},
		{"h1", "w", "private-work", "obsolete", nil},
		{"h1", "w", "private-work", "", nil},
		{"h1", "w", "private-work", "", map[string]any{"task": false}},
	} {
		if receipt, err := s.AdmitManual(t.Context(), tc.user, tc.workspace, tc.slug, tc.inputs, tc.hash, 0); receipt.ID != "" || err == nil {
			t.Fatalf("invalid request admitted: %+v %v", receipt, err)
		}
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied request created attempt: %d %v", count, err)
	}
}

func TestWorkflowProjectionRollbackPreservesPendingAuthority(t *testing.T) {
	for _, table := range []string{"access_attempts", "restricted_workflow_jobs"} {
		t.Run(table, func(t *testing.T) {
			s, _ := fixture(t)
			receipt := admitFixtureJob(t, s)
			if _, err := s.db.ExecContext(t.Context(), `UPDATE work_items SET state='failed' WHERE id=?`, receipt.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(t.Context(), "CREATE TRIGGER fixture_reject BEFORE UPDATE ON "+table+" BEGIN SELECT RAISE(ABORT,'fixture projection failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err := s.runtime.FlushRunOutcomes(t.Context()); err == nil {
				t.Fatal("failed projection reported success")
			}
			var state string
			var revoked int
			if err := s.db.QueryRowContext(t.Context(), `SELECT j.state,a.revoked_at IS NOT NULL FROM restricted_workflow_jobs j JOIN access_attempts a ON a.id=j.origin_attempt_id WHERE j.id=?`, receipt.ID).Scan(&state, &revoked); err != nil || state != "pending" || revoked != 0 {
				t.Fatalf("partial projection: %s revoked=%d %v", state, revoked, err)
			}
			if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER fixture_reject"); err != nil {
				t.Fatal(err)
			}
			if err := s.runtime.FlushRunOutcomes(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(t.Context(), `SELECT state FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("retry failed: %s %v", state, err)
			}
		})
	}
}

func TestWorkflowUnavailableSchedulingStorageReturnsError(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM work_items WHERE id=?`, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiptForActor(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("unscheduled receipt exposed: %v", err)
	}
	if err := s.markJobRunning(t.Context(), dispatch.Assignment{Item: &work.Item{ID: receipt.ID, DomainID: receipt.ID}}); err == nil {
		t.Fatal("unowned assignment marked running")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.markJobRunning(ctx, dispatch.Assignment{Item: &work.Item{ID: receipt.ID, DomainID: receipt.ID}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update: %v", err)
	}
	j, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, field := range []string{"fire", "expiry"} {
		copy := j
		if field == "fire" {
			copy.FireAt = "invalid"
		} else {
			copy.Expires = "invalid"
		}
		if err = s.acceptWork(t.Context(), tx, copy); err == nil || !strings.Contains(err.Error(), "parsing time") {
			t.Fatalf("invalid %s accepted: %v", field, err)
		}
	}
}
