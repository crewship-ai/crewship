package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// openTrustGateTestDB adds the tables CreateApproval touches on the trust
// path: crews (for the autonomy check) and inbox_items (to prove a fired
// grant writes NO blocking card).
func openTrustGateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTrustGrantTestDB(t)
	// The shared test schema declares crews as (id, workspace_id) only;
	// the autonomy dial is a v101 column the real schema has.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE crews ADD COLUMN autonomy_level TEXT NOT NULL DEFAULT 'guided'`); err != nil {
		t.Fatalf("add autonomy_level: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS inbox_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    kind                TEXT NOT NULL,
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL,
    priority            TEXT NOT NULL,
    blocking            INTEGER NOT NULL DEFAULT 0,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);`); err != nil {
		t.Fatalf("trust gate schema: %v", err)
	}
	return db
}

// seedTrustRun fabricates the run row CreateApproval reads its routine
// identity from. autonomy is the invoking crew's dial; "" means the run
// has no invoking crew at all.
func seedTrustRun(t *testing.T, db *sql.DB, runID, hash, autonomy string) {
	t.Helper()
	crewID := ""
	if autonomy != "" {
		crewID = "cr_" + runID
		if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, autonomy_level) VALUES (?, 'ws_test', ?)`,
			crewID, autonomy); err != nil {
			t.Fatalf("seed crew: %v", err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, definition_hash, status, started_at, invoking_crew_id)
VALUES (?, 'ws_test', 'pl1', 'triage', ?, 'running', datetime('now'), ?)`,
		runID, hash, nullableStr(crewID)); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func waitpointRow(t *testing.T, db *sql.DB, token string) (status, decidedBy, payload string) {
	t.Helper()
	var by, pl sql.NullString
	if err := db.QueryRow(`SELECT status, decided_by_user_id, decision_payload FROM pipeline_waitpoints WHERE token = ?`,
		token).Scan(&status, &by, &pl); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	return status, by.String, pl.String
}

func inboxCount(t *testing.T, db *sql.DB, token string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, token).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

// TestCreateApproval_TrustGrant is the feature's contract at the point it
// actually changes behaviour: a gate covered by a standing grant resolves
// itself instead of parking the run and paging a human.
func TestCreateApproval_TrustGrant(t *testing.T) {
	ctx := context.Background()

	t.Run("granted gate resolves without an inbox card", func(t *testing.T) {
		db := openTrustGateTestDB(t)
		grants := NewTrustGrantStore(db)
		mustGrant(t, grants, baseGrant())
		seedTrustRun(t, db, "run_ok", "hashA", "guided")

		store := NewSQLWaitpointStore(db)
		defer store.Close()

		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:   "ws_test",
			PipelineRunID: "run_ok",
			StepID:        "publish",
			Prompt:        "Publish the comment?",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}

		status, decidedBy, payload := waitpointRow(t, db, token)
		if status != "approved" {
			t.Errorf("waitpoint status = %q, want approved — the standing grant did not fire", status)
		}
		if decidedBy != "usr1" {
			t.Errorf("decided_by_user_id = %q, want the granting operator usr1 — an auto-approval must stay attributable", decidedBy)
		}
		if !strings.Contains(payload, "auto_approved") || !strings.Contains(payload, "wtg_") {
			t.Errorf("decision_payload = %q, want it to name the grant that fired", payload)
		}
		if n := inboxCount(t, db, token); n != 0 {
			t.Errorf("inbox cards for an auto-approved gate: %d, want 0 — the whole point is to stop paging the operator", n)
		}

		// And the run must not park: WaitFor resolves from the row.
		// Bounded, because the failure mode under test IS an unbounded
		// park — an unbounded wait here would hang the suite instead of
		// reporting it.
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		approved, err := store.WaitFor(waitCtx, token)
		if err != nil {
			t.Fatalf("WaitFor: %v — an auto-approved gate still parked the run", err)
		}
		if !approved {
			t.Error("WaitFor did not resolve an auto-approved waitpoint")
		}
	})

	t.Run("ungranted gate still blocks and still pages", func(t *testing.T) {
		db := openTrustGateTestDB(t)
		seedTrustRun(t, db, "run_block", "hashA", "guided")

		store := NewSQLWaitpointStore(db)
		defer store.Close()

		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:   "ws_test",
			PipelineRunID: "run_block",
			StepID:        "publish",
			Prompt:        "Publish the comment?",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}
		if status, _, _ := waitpointRow(t, db, token); status != "pending" {
			t.Errorf("waitpoint status = %q, want pending", status)
		}
		if n := inboxCount(t, db, token); n != 1 {
			t.Errorf("inbox cards = %d, want 1", n)
		}
	})

	// A strict crew has opted out of exactly this kind of shortcut. v106
	// made the same call for self-learning: "a strict crew cannot
	// self-learn ... regardless of this flag".
	t.Run("strict crew ignores a live grant", func(t *testing.T) {
		db := openTrustGateTestDB(t)
		grants := NewTrustGrantStore(db)
		mustGrant(t, grants, baseGrant())
		seedTrustRun(t, db, "run_strict", "hashA", "strict")

		store := NewSQLWaitpointStore(db)
		defer store.Close()

		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:    "ws_test",
			PipelineRunID:  "run_strict",
			StepID:         "publish",
			InvokingCrewID: "cr_run_strict",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}
		if status, _, _ := waitpointRow(t, db, token); status != "pending" {
			t.Errorf("waitpoint status = %q, want pending — a strict crew must not honour standing grants", status)
		}
	})

	// The offer side: an operator is only invited to stop being asked
	// once they have actually approved this gate repeatedly.
	t.Run("inbox card carries the trust offer past the threshold", func(t *testing.T) {
		db := openTrustGateTestDB(t)
		for i, runID := range []string{"run_p1", "run_p2", "run_p3"} {
			seedTrustRun(t, db, runID, "hashA", "guided")
			if _, err := db.Exec(`
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES (?, 'ws_test', ?, 'publish', 'approval', 'approved', datetime('now','+1 day'))`,
				"tok_prior_"+runID, runID); err != nil {
				t.Fatalf("seed prior approval %d: %v", i, err)
			}
		}
		seedTrustRun(t, db, "run_offer", "hashA", "guided")

		store := NewSQLWaitpointStore(db)
		defer store.Close()

		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:   "ws_test",
			PipelineRunID: "run_offer",
			StepID:        "publish",
			Prompt:        "Publish the comment?",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}

		var payloadJSON string
		if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE source_id = ?`, token).Scan(&payloadJSON); err != nil {
			t.Fatalf("read inbox payload: %v", err)
		}
		var payload struct {
			TrustOffer struct {
				Eligible       bool   `json:"eligible"`
				PriorApprovals int    `json:"prior_approvals"`
				DefinitionHash string `json:"definition_hash"`
				StepID         string `json:"step_id"`
				PipelineID     string `json:"pipeline_id"`
			} `json:"trust_offer"`
		}
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			t.Fatalf("decode payload %q: %v", payloadJSON, err)
		}
		if !payload.TrustOffer.Eligible {
			t.Error("trust_offer.eligible = false after 3 prior approvals — the operator is never offered the shortcut")
		}
		if payload.TrustOffer.PriorApprovals != 3 {
			t.Errorf("trust_offer.prior_approvals = %d, want 3", payload.TrustOffer.PriorApprovals)
		}
		// The card must carry everything the grant call needs, so the UI
		// never has to re-derive which definition was on screen.
		if payload.TrustOffer.DefinitionHash != "hashA" || payload.TrustOffer.StepID != "publish" || payload.TrustOffer.PipelineID != "pl1" {
			t.Errorf("trust_offer missing grant coordinates: %+v", payload.TrustOffer)
		}
	})

	t.Run("below the threshold there is no offer", func(t *testing.T) {
		db := openTrustGateTestDB(t)
		seedTrustRun(t, db, "run_few", "hashA", "guided")

		store := NewSQLWaitpointStore(db)
		defer store.Close()

		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:   "ws_test",
			PipelineRunID: "run_few",
			StepID:        "publish",
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}
		var payloadJSON string
		if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE source_id = ?`, token).Scan(&payloadJSON); err != nil {
			t.Fatalf("read inbox payload: %v", err)
		}
		if strings.Contains(payloadJSON, `"eligible":true`) {
			t.Errorf("offered a standing grant on first sight: %s", payloadJSON)
		}
	})
}
