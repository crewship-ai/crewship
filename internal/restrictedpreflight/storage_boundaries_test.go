//go:build linux

package restrictedpreflight

import (
	"errors"
	"strings"
	"testing"
)

func TestUnconfiguredPreflightCannotProduceWork(t *testing.T) {
	for _, service := range []*Service{nil, {}} {
		if receipt, err := service.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); !errors.Is(err, ErrNoWork) || receipt.ID != "" {
			t.Fatalf("unconfigured service produced work: %+v, %v", receipt, err)
		}
	}
}

func TestPreflightStorageFailuresDoNotPublishPrivateWork(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"reservation write", `CREATE TRIGGER reject_reservation BEFORE INSERT ON restricted_preflight_reservations BEGIN SELECT RAISE(ABORT, 'fixture disk write failure'); END`},
		{"workflow write", `CREATE TRIGGER reject_workflow BEFORE INSERT ON restricted_workflow_jobs BEGIN SELECT RAISE(ABORT, 'fixture disk write failure'); END`},
		{"missing routine", `DELETE FROM pipelines WHERE id='routine'`},
		{"oversized encoded input", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, executor := fixture(t)
			if tc.query != "" {
				if _, err := s.DB.Exec(tc.query); err != nil {
					t.Fatal(err)
				}
			} else {
				// Fits the source text bound, but JSON escaping exceeds the
				// immutable input budget. It must not prepare a chat or run.
				if _, err := s.DB.Exec(`UPDATE missions SET description=? WHERE id='i1'`, strings.Repeat("\x01", 3000)); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
			if err == nil || receipt.ID != "" {
				t.Fatalf("failed claim returned a receipt: %+v, %v", receipt, err)
			}
			if executor.calls != 0 || count(t, s.DB, "restricted_preflight_reservations") != 0 || count(t, s.DB, "restricted_workflow_jobs") != 0 {
				t.Fatal("failed claim published private execution")
			}
		})
	}
}

func TestPrivateSourceReadersFailClosedOnStorageLoss(t *testing.T) {
	s, _ := fixture(t)
	src, err := readSource(t.Context(), s.DB, "h1", "w", "agent", "i1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`DROP TABLE restricted_preflight_reservations`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.duplicate(t.Context(), src, "h1", "issue-work"); err == nil || found {
		t.Fatalf("lost dedup storage = %v, %v", found, err)
	}
	if err := CheckSource(t.Context(), s.DB, "missing-origin"); !errors.Is(err, ErrNoWork) {
		t.Fatalf("lost source binding = %v", err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSource(t.Context(), s.DB, "h1", "w", "agent", "i1"); err == nil {
		t.Fatal("closed DB returned an authorized source")
	}
	if _, _, err := s.duplicate(t.Context(), src, "h1", "issue-work"); err == nil {
		t.Fatal("closed DB returned dedup state")
	}
	if err := legacyBusy(t.Context(), s.DB, "i1"); err == nil {
		t.Fatal("closed DB treated legacy owner as absent")
	}
}

func TestPreflightCapacityAndBudgetQueriesFailClosed(t *testing.T) {
	for _, table := range []string{"restricted_preflight_reservations", "budget_limits"} {
		t.Run(table, func(t *testing.T) {
			s, executor := fixture(t)
			src, err := readSource(t.Context(), s.DB, "h1", "w", "agent", "i1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			tx, err := s.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := cheap(t.Context(), tx, src, true); err == nil {
				t.Fatal("unreadable admission state treated as available capacity")
			}
			if executor.calls != 0 {
				t.Fatal("failed admission started execution")
			}
		})
	}
}

func TestCheckSourceRejectsChangedOrRevokedIssue(t *testing.T) {
	for _, change := range []string{`UPDATE missions SET description='changed' WHERE id='i1'`, `DELETE FROM access_grants WHERE project_id='p1'`} {
		t.Run(change, func(t *testing.T) {
			s, executor := fixture(t)
			receipt, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
			if err != nil {
				t.Fatal(err)
			}
			var origin string
			if err := s.DB.QueryRow(`SELECT origin_attempt_id FROM restricted_preflight_reservations WHERE workflow_id=?`, receipt.ID).Scan(&origin); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(change); err != nil {
				t.Fatal(err)
			}
			if err := CheckSource(t.Context(), s.DB, origin); !errors.Is(err, ErrNoWork) {
				t.Fatalf("revoked source accepted: %v", err)
			}
			if executor.calls != 0 {
				t.Fatal("source check started execution")
			}
		})
	}
}
