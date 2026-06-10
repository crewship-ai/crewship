package server

// Boot-wiring test for the stuck-QUEUED assignment sweeper (W1 of
// RELEASE-1.0-HARDENING). The sweeper itself is unit-tested in
// internal/api/assignments_stuck_sweeper_test.go; what this test pins
// is the production wiring: Server.Start must start the sweeper, with
// shutdown bound to the run context. The observable is end-to-end — a
// QUEUED assignment stranded in the DB (simulating a crash before the
// completion-path pump could fire) transitions out of QUEUED after
// boot, with no HTTP call ever touching the queue.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

// seedStrandedQueuedAssignment writes the minimal workspace → crew →
// agent → chat → assignment chain with the assignment QUEUED and
// queued_at backdated far past any stale threshold — the exact row
// shape a pre-restart crash leaves behind.
func seedStrandedQueuedAssignment(t *testing.T, db *sql.DB) (assignmentID string) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\nquery: %s", err, query)
		}
	}
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('u_sweepboot', 'sweepboot@example.com', 'Sweep Boot')`)
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_sweepboot', 'Sweep Boot', 'sweep-boot')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm_sweepboot', 'ws_sweepboot', 'u_sweepboot', 'OWNER')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
	          VALUES ('crew_sweepboot', 'ws_sweepboot', 'Sweep Boot', 'sweep-boot', '🧹', '#000', 4096, 2)`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role,
	                              cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled)
	          VALUES ('agent_sweepboot', 'crew_sweepboot', 'ws_sweepboot', 'Sweeper Agent', 'agent-sweep-boot', 'Worker', 'AGENT',
	                  'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6', 'standard', 60, 0)`)
	mustExec(`INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
	          VALUES ('chat_sweepboot', 'agent_sweepboot', 'ws_sweepboot', 'sweep boot', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`)
	// queued_at uses the dispatcher's 'YYYY-MM-DD HH:MM:SS.SSS' shape,
	// backdated an hour — stale under any threshold the boot wiring
	// could plausibly use.
	queuedAt := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.000")
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, queued_at, created_at)
	          VALUES ('a_sweepboot', 'ws_sweepboot', 'chat_sweepboot', 'agent_sweepboot', 'agent_sweepboot', 'stranded task', 'QUEUED', ?, datetime('now'))`, queuedAt)
	return "a_sweepboot"
}

func TestStart_StuckQueueSweeper_RescuesStrandedQueuedRow(t *testing.T) {
	// Deliberately NOT t.Parallel(): the test shortens the package-level
	// sweeper cadence vars so the integration finishes in milliseconds
	// instead of the production 60s first tick.
	origInterval, origStale := stuckQueueSweepInterval, stuckQueueStaleAfter
	stuckQueueSweepInterval, stuckQueueStaleAfter = 50*time.Millisecond, 1*time.Millisecond
	t.Cleanup(func() {
		stuckQueueSweepInterval, stuckQueueStaleAfter = origInterval, origStale
	})

	db := openTestDB(t)
	assignmentID := seedStrandedQueuedAssignment(t, db)

	// Unix sockets have a tight path-length limit (~104 chars on macOS),
	// shorter than t.TempDir() can produce. Short unique name in /tmp.
	sockPath := filepath.Join("/tmp", "cs-sweep-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0 // ephemeral — the HTTP surface is irrelevant here

	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: db})
	t.Cleanup(s.StopBackground)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// The sweeper's observable side effect: the stranded row leaves
	// QUEUED (it gets pumped → claimed RUNNING → terminal FAILED here,
	// since the test server has no container provider — either way the
	// queue moved, which is the recovery contract).
	deadline := time.Now().Add(5 * time.Second)
	swept := false
	for time.Now().Before(deadline) {
		var status string
		if err := db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, assignmentID).Scan(&status); err != nil {
			t.Fatalf("poll assignment status: %v", err)
		}
		if status != "QUEUED" {
			swept = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
		// Start returned via Shutdown — clean exit on ctx cancel.
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not shut down within 10s after ctx cancel")
	}

	if !swept {
		t.Fatalf("stranded QUEUED assignment was not swept within 5s of server start — StartStuckQueueSweeper is not wired into Server.Start")
	}
}
