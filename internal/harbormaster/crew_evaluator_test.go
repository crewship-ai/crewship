package harbormaster

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCrewEvaluatorExplicitOptInAndTenantFence(t *testing.T) {
	db := testutil.MigratedDB(t)
	for _, statement := range []string{`INSERT INTO workspaces(id,name,slug) VALUES('gate-ws','Gate','gate-ws')`, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('gate-crew','gate-ws','Gate crew','gate-crew')`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	evaluate := func() bool {
		t.Helper()
		e, err := EvaluatorForCrew(t.Context(), db.DB, "gate-ws", "gate-crew", nil)
		if err != nil {
			t.Fatal(err)
		}
		matched, _, _ := e.Evaluate(t.Context(), "agent_run", map[string]any{"agent_slug": "synthetic", "agent_role": "WORKER"})
		return matched
	}
	if evaluate() {
		t.Fatal("default rules unexpectedly match whole run")
	}
	if _, err := db.Exec(`UPDATE crews SET whole_run_approval=1 WHERE id='gate-crew'`); err != nil {
		t.Fatal(err)
	}
	if !evaluate() {
		t.Fatal("opted-in whole run did not match")
	}
	for _, scope := range [][2]string{{"foreign", "gate-crew"}, {"gate-ws", "missing"}} {
		if _, err := EvaluatorForCrew(t.Context(), db.DB, scope[0], scope[1], nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign/missing crew: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE crews SET deleted_at='2026-10-16' WHERE id='gate-crew'`); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluatorForCrew(t.Context(), db.DB, "gate-ws", "gate-crew", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted crew: %v", err)
	}
}
