package pipeline

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"
)

// waitOnlyTrustDSL has nothing but the gate: no agent step, so the run
// needs no provider and the only thing under test is what the executor
// does with the waitpoint.
const waitOnlyTrustDSL = `{
  "dsl_version": "1.0",
  "name": "trusted-gate",
  "steps": [
    {"id": "gate", "type": "wait", "wait": {"kind": "approval", "approval_prompt": "ok?"}, "timeout_seconds": 3600}
  ]
}`

func addTrustGrantTable(t *testing.T, db *sql.DB) {
	t.Helper()
	// The shared test schema declares crews as (id, workspace_id); the
	// autonomy dial is a v101 column the real schema has. Without it the
	// trust lookup fails closed — correctly, but for a reason that only
	// exists in the rig.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE crews ADD COLUMN autonomy_level TEXT NOT NULL DEFAULT 'guided'`); err != nil {
		t.Fatalf("add autonomy_level: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS waitpoint_trust_grants (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL,
    pipeline_id        TEXT NOT NULL,
    step_id            TEXT NOT NULL,
    definition_hash    TEXT NOT NULL,
    granted_by_user_id TEXT NOT NULL,
    granted_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    reason             TEXT,
    prior_approvals    INTEGER NOT NULL DEFAULT 0,
    max_uses           INTEGER,
    uses               INTEGER NOT NULL DEFAULT 0,
    expires_at         TEXT,
    revoked_at         TEXT,
    revoked_by_user_id TEXT,
    revoke_reason      TEXT
);`); err != nil {
		t.Fatalf("trust grant schema: %v", err)
	}
}

// TestRun_TrustedGate_CompletesInsteadOfParking is the executor-level
// half of standing grants, and the half unit-testing CreateApproval in
// isolation cannot reach.
//
// The park decision used to read `park := !in.resume` — a fresh run
// parked unconditionally, because before standing grants a fresh run's
// waitpoint was ALWAYS pending. A grant breaks that assumption: the
// waitpoint is written already-approved, so parking strands the run in
// `waiting` with nothing left to wake it. The approval sweeper only
// looks at pending rows, so it does not even time out — the run hangs
// forever, and the operator is neither asked nor served.
//
// Found by driving the real CLI against a real server, not by any unit
// test: every layer below passed while the feature was unusable.
func TestRun_TrustedGate_CompletesInsteadOfParking(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	addTrustGrantTable(t, db)
	ctx := context.Background()

	deps := fullExecutorDeps(t, db, newMockRunner())
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "trusted-gate", waitOnlyTrustDSL)

	// The operator has already said "stop asking me" for this gate on
	// this exact routine body.
	grants := NewTrustGrantStore(db)
	if _, err := grants.Grant(ctx, GrantInput{
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		StepID:          "gate",
		DefinitionHash:  p.DefinitionHash,
		GrantedByUserID: "usr1",
		Reason:          "approved 12x",
		PriorApprovals:  12,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(NewScheduleStore(db), deps.Store, exec, logger)

	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.fireOne(ctx, &Schedule{
			ID:               "psched_trusted",
			WorkspaceID:      "ws_test",
			TargetPipelineID: p.ID,
			CronExpr:         "0 8 * * *",
			Timezone:         "UTC",
		})
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("fireOne did not return — a trusted gate is blocking the run")
	}

	// Nothing may be left parked: a trusted gate that parks is strictly
	// worse than one that asks, because nobody will ever answer it.
	active, err := deps.RunStore.ListActive(ctx, "ws_test")
	if err != nil {
		t.Fatalf("list active runs: %v", err)
	}
	for _, r := range active {
		if r.Status == RunStatusWaiting {
			t.Fatalf("run %s parked at %q despite a live standing grant — it will hang until the approval times out, and the sweeper ignores already-decided waitpoints",
				r.ID, r.CurrentStepID)
		}
	}

	// "Nothing parked" is satisfied by a FAILED run too, so assert the
	// terminal status the feature actually promises: a trusted gate lets
	// the routine finish, it does not merely stop blocking it.
	var runStatus string
	if err := db.QueryRowContext(ctx, `
SELECT status FROM pipeline_runs WHERE pipeline_id = ? ORDER BY started_at DESC LIMIT 1`, p.ID).Scan(&runStatus); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if RunStatus(runStatus) != RunStatusCompleted {
		t.Errorf("run status = %q, want %q — a trusted gate must carry the routine through, not just fail somewhere other than the gate",
			runStatus, RunStatusCompleted)
	}

	// The waitpoint still exists and is approved — the audit trail is the
	// point; it is the PARKING that must not happen, not the record.
	var status, payload string
	if err := db.QueryRowContext(ctx, `
SELECT status, COALESCE(decision_payload, '') FROM pipeline_waitpoints
 WHERE step_id = 'gate' ORDER BY created_at DESC LIMIT 1`).Scan(&status, &payload); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if status != "approved" {
		t.Errorf("waitpoint status = %q, want approved", status)
	}
	if payload == "" {
		t.Error("auto-approved waitpoint carries no decision payload — the run history cannot show which grant stood behind it")
	}
}
