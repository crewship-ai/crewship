package pipeline

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// openTrustGrantTestDB gives the grant store its table. The rest of the
// pipeline test schema comes from openStoreTestDB.
func openTrustGrantTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openResumeTestDB(t)
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
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_waitpoint_trust_grants_live
    ON waitpoint_trust_grants (workspace_id, pipeline_id, step_id, definition_hash)
    WHERE revoked_at IS NULL;`); err != nil {
		t.Fatalf("trust grant schema: %v", err)
	}
	return db
}

func mustGrant(t *testing.T, s *TrustGrantStore, in GrantInput) string {
	t.Helper()
	id, err := s.Grant(context.Background(), in)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	return id
}

func baseGrant() GrantInput {
	return GrantInput{
		WorkspaceID:     "ws_test",
		PipelineID:      "pl1",
		StepID:          "publish",
		DefinitionHash:  "hashA",
		GrantedByUserID: "usr1",
		Reason:          "approved 10x, always the same diff",
		PriorApprovals:  10,
	}
}

// TestTrustGrant_Consume covers the whole reason this table exists: a
// grant must fire for exactly the gate + definition it was given for,
// and must stop firing the moment any of its bounds is crossed.
func TestTrustGrant_Consume(t *testing.T) {
	ctx := context.Background()

	t.Run("no grant means the gate still asks", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		_, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("consumed a grant that was never issued")
		}
	})

	t.Run("granted gate auto-approves and counts the use", func(t *testing.T) {
		db := openTrustGrantTestDB(t)
		s := NewTrustGrantStore(db)
		id := mustGrant(t, s, baseGrant())

		use, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if !ok {
			t.Fatal("live grant did not fire")
		}
		if use.GrantID != id {
			t.Errorf("grant id = %q, want %q", use.GrantID, id)
		}
		// The approval this produces is attributed to a human; without
		// the granter it would read as having no author at all.
		if use.GrantedByUserID != "usr1" {
			t.Errorf("granted_by_user_id = %q, want usr1", use.GrantedByUserID)
		}
		if use.Uses != 1 {
			t.Errorf("use.Uses = %d, want 1 (the count including this fire)", use.Uses)
		}
		var uses int
		if err := db.QueryRow(`SELECT uses FROM waitpoint_trust_grants WHERE id = ?`, id).Scan(&uses); err != nil {
			t.Fatalf("read uses: %v", err)
		}
		if uses != 1 {
			t.Errorf("uses = %d, want 1 — the audit cannot show how often trust fired", uses)
		}
	})

	// The load-bearing property. Editing a routine mints a new
	// definition_hash, so the grant the operator gave for the old
	// content must not carry onto the new content.
	t.Run("edited routine does not inherit the grant", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		mustGrant(t, s, baseGrant())

		_, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashB")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("grant fired for a definition the operator never saw — trust survived an edit")
		}
	})

	t.Run("a sibling gate in the same routine is untouched", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		mustGrant(t, s, baseGrant())

		_, ok, err := s.Consume(ctx, "ws_test", "pl1", "delete_everything", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("trusting one gate trusted another gate in the same routine")
		}
	})

	t.Run("another workspace cannot borrow the grant", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		mustGrant(t, s, baseGrant())

		_, ok, err := s.Consume(ctx, "ws_other", "pl1", "publish", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("grant crossed a workspace boundary")
		}
	})

	t.Run("revoked grant stops firing", func(t *testing.T) {
		db := openTrustGrantTestDB(t)
		s := NewTrustGrantStore(db)
		id := mustGrant(t, s, baseGrant())

		revoked, err := s.Revoke(ctx, "ws_test", id, "usr2", "changed my mind")
		if err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if !revoked {
			t.Fatal("Revoke reported no row")
		}
		_, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("revoked grant still fired")
		}
	})

	t.Run("expired grant stops firing", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		past := time.Now().Add(-time.Hour)
		in := baseGrant()
		in.ExpiresAt = &past
		mustGrant(t, s, in)

		_, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA")
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if ok {
			t.Error("expired grant still fired")
		}
	})

	t.Run("max_uses is a hard ceiling", func(t *testing.T) {
		s := NewTrustGrantStore(openTrustGrantTestDB(t))
		max := 2
		in := baseGrant()
		in.MaxUses = &max
		mustGrant(t, s, in)

		for i := 1; i <= max; i++ {
			if _, ok, err := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA"); err != nil || !ok {
				t.Fatalf("use %d: ok=%v err=%v, want a fire", i, ok, err)
			}
		}
		if _, ok, _ := s.Consume(ctx, "ws_test", "pl1", "publish", "hashA"); ok {
			t.Errorf("grant fired %d times, max_uses was %d", max+1, max)
		}
	})
}

// TestTrustGrant_ConsumeIsAtomic pins the single-statement
// UPDATE..RETURNING: max_uses must be a ceiling under concurrent gates,
// not a suggestion. Two runs of the same routine hitting the same gate
// at once is the ordinary case, not an exotic one.
//
// Note the test DB is :memory: with a one-connection pool, which
// serialises writers — so this asserts the counting semantics rather
// than proving the statement is race-free on a file DB. The guarantee
// it protects is that no read-modify-write pair was introduced here.
func TestTrustGrant_ConsumeIsAtomic(t *testing.T) {
	s := NewTrustGrantStore(openTrustGrantTestDB(t))
	max := 5
	in := baseGrant()
	in.MaxUses = &max
	mustGrant(t, s, in)

	const racers = 25
	var wg sync.WaitGroup
	fired := make(chan struct{}, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.Consume(context.Background(), "ws_test", "pl1", "publish", "hashA"); err == nil && ok {
				fired <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(fired)

	if got := len(fired); got != max {
		t.Errorf("grant fired %d times under %d concurrent gates, want exactly max_uses=%d", got, racers, max)
	}
}

// TestTrustGrant_PriorApprovals drives the offer side: the operator is
// only asked "stop asking me?" once they have actually approved this
// exact gate on this exact definition several times. Approvals of a
// different definition must not count towards the offer — otherwise a
// routine could be edited into something new and immediately present
// itself as long-trusted.
func TestTrustGrant_PriorApprovals(t *testing.T) {
	db := openTrustGrantTestDB(t)
	s := NewTrustGrantStore(db)
	ctx := context.Background()

	seed := func(runID, hash, status string) {
		t.Helper()
		if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, definition_hash, status, started_at)
VALUES (?, 'ws_test', 'pl1', 'triage', ?, 'completed', datetime('now'))`, runID, hash); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		if _, err := db.Exec(`
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES (?, 'ws_test', ?, 'publish', 'approval', ?, datetime('now','+1 day'))`,
			"tok_"+runID, runID, status); err != nil {
			t.Fatalf("seed waitpoint: %v", err)
		}
	}

	seed("run1", "hashA", "approved")
	seed("run2", "hashA", "approved")
	seed("run3", "hashA", "denied")   // a denial is not a vote of confidence
	seed("run4", "hashB", "approved") // different definition
	seed("run5", "hashA", "timed_out")

	got, err := s.PriorApprovals(ctx, "ws_test", "pl1", "publish", "hashA")
	if err != nil {
		t.Fatalf("PriorApprovals: %v", err)
	}
	if got != 2 {
		t.Errorf("prior approvals = %d, want 2 (denials, timeouts and other definitions must not count)", got)
	}
}
