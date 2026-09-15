package pipeline

import (
	"context"
	"testing"
)

// TestCreateApproval_InvokingCrewCannotRelaxStrictAuthor is the trust-side
// half of the invoking-identity fix (AWX/Omarchy iteration 1, opponent
// finding K1).
//
// routineTrust used to consult ONLY the invoking crew's autonomy dial when
// the run row carried one, and the author crew's only as a fallback. So a
// run attributed to a `full` crew let a standing grant fire on a gate of a
// routine whose author crew is `strict` — the posture that governs the
// execution, and the one that had opted out of every shortcut. Until the
// public run route stopped reading X-Crewship-Invoking-Crew from arbitrary
// members, that invoking crew could be anything a caller typed; after that
// fix it is a verified crew, but a verified sibling must still not be able
// to talk a strict author's gate past its operator.
//
// The invoking crew's dial still applies when the author is NOT strict —
// the second case pins that down so this stays a tightening, not a rewrite.
func TestCreateApproval_InvokingCrewCannotRelaxStrictAuthor(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T, authorAutonomy string) (*SQLWaitpointStore, string) {
		t.Helper()
		db := openTrustGateTestDB(t)
		for _, row := range [][2]string{{"cr_author", authorAutonomy}, {"cr_invoker", "full"}} {
			if _, err := db.ExecContext(ctx, `INSERT INTO crews (id, workspace_id, autonomy_level) VALUES (?, 'ws_test', ?)`,
				row[0], row[1]); err != nil {
				t.Fatalf("seed crew %s: %v", row[0], err)
			}
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, author_crew_id)
VALUES ('pl1', 'ws_test', 'triage', 'Triage', '{}', 'hashA', 'cr_author')`); err != nil {
			t.Fatalf("seed pipeline: %v", err)
		}
		mustGrant(t, NewTrustGrantStore(db), baseGrant())
		// The run row says a `full` crew invoked it — exactly what the
		// executor persists from RunInput.InvokingCrewID.
		if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, definition_hash, status, started_at, invoking_crew_id)
VALUES ('run_x', 'ws_test', 'pl1', 'triage', 'hashA', 'running', datetime('now'), 'cr_invoker')`); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		store := NewSQLWaitpointStore(db)
		t.Cleanup(func() { store.Close() })
		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:    "ws_test",
			PipelineRunID:  "run_x",
			StepID:         "publish",
			Prompt:         "Publish?",
			InvokingCrewID: "cr_invoker",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}
		return store, token
	}

	t.Run("strict author keeps the human even when a full crew invoked", func(t *testing.T) {
		store, token := seed(t, "strict")
		if status, _, _ := waitpointRow(t, store.db, token); status != "pending" {
			t.Errorf("waitpoint status = %q, want pending — a strict author crew's gate must not be routed through a grant because the invoking crew is %q", status, "full")
		}
	})

	t.Run("non-strict author still lets the invoking crew's grant fire", func(t *testing.T) {
		store, token := seed(t, "guided")
		if status, _, _ := waitpointRow(t, store.db, token); status != "approved" {
			t.Errorf("waitpoint status = %q, want approved — the existing cross-crew grant path must keep working when nobody is strict", status)
		}
	})
}
