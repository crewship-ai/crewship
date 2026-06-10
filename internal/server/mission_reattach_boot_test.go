package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
)

// W5 boot wiring: the orchestrator-level tests for
// ReattachInProgressMissions run against a hand-rolled schema; this test
// runs the same boot-time scan against the real migrated database so the
// production SQL (missions columns, status values, ordering) stays pinned.
// Mirrors the TestRecoverOrphanedRuns_MarksRunningCancelled setup, which
// covers the recovery step that precedes this one in Start().
func TestBootReattach_InProgressMissionGetsDriver(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "reattach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := logging.New("error", "json", nil)
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('cr1','w1','Crew','crew',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, created_at, updated_at)
		VALUES ('lead1','w1','cr1','Lead','lead','LEAD','IDLE',?,?)`, now, now)
	// The stranded mission: IN_PROGRESS in the DB, but the process that ran
	// its loop is gone (this is a fresh Server).
	mustExec(t, db.DB, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1','w1','cr1','lead1','tr1','Stranded mission','IN_PROGRESS',?,?)`, now, now)
	// Terminal mission must NOT get a driver.
	mustExec(t, db.DB, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m2','w1','cr1','lead1','tr2','Done mission','COMPLETED',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-mission-reattach-32ch"
	s := New(cfg, logger, &Deps{DB: db.DB})
	t.Cleanup(func() {
		s.missionEngine.Shutdown()
		s.StopBackground()
	})

	n := s.missionEngine.ReattachInProgressMissions(context.Background())
	if n != 1 {
		t.Fatalf("ReattachInProgressMissions = %d, want 1", n)
	}

	// Idempotent on a second boot-style invocation (e.g. the scan racing an
	// API start): the already-attached mission is skipped, not restarted.
	if n := s.missionEngine.ReattachInProgressMissions(context.Background()); n != 0 {
		t.Errorf("second ReattachInProgressMissions = %d, want 0", n)
	}
}
