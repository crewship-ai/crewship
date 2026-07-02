package server

// Boot-wiring test for interrupted-RUNNING assignment recovery.
//
// A crash while an assignment is RUNNING leaves the row RUNNING forever:
// the dispatch goroutine died with the process, recoverOrphanedRuns only
// rewrites journal_entries/agents, and the stuck-QUEUED sweeper explicitly
// skips RUNNING rows. Because claimCrewSlot counts status='RUNNING' rows
// against the crew budget, the orphaned row permanently leaks a crew
// concurrency slot — every later delegation queues forever.
//
// This test pins the production wiring: Server.Start must fail such rows
// ("interrupted by server restart") and, by freeing the slot, let the
// queue pump promote a stranded QUEUED row on the same crew. The unit
// behavior lives in internal/api; what matters here is the end-to-end
// boot observable with no HTTP call ever touching the queue.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

// seedOrphanedRunningAssignment writes the minimal workspace → crew →
// agent → chat chain plus TWO assignments on a budget-1 crew:
//
//   - a_runboot_orphan: RUNNING, started_at backdated — the exact row a
//     pre-restart crash leaves behind (its driver goroutine is gone).
//   - a_runboot_queued: QUEUED, stranded behind the orphan. It can only
//     move if boot recovery actually frees the leaked slot.
func seedOrphanedRunningAssignment(t *testing.T, db *sql.DB) (orphanID, queuedID string) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\nquery: %s", err, query)
		}
	}
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('u_runboot', 'runboot@example.com', 'Run Boot')`)
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_runboot', 'Run Boot', 'run-boot')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm_runboot', 'ws_runboot', 'u_runboot', 'OWNER')`)
	// budget = 1 via max_concurrent_agents so the single orphaned RUNNING
	// row saturates the crew.
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus, max_concurrent_agents)
	          VALUES ('crew_runboot', 'ws_runboot', 'Run Boot', 'run-boot', '⚓', '#000', 4096, 2, 1)`)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role,
	                              cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled)
	          VALUES ('agent_runboot', 'crew_runboot', 'ws_runboot', 'Runboot Agent', 'agent-run-boot', 'Worker', 'AGENT',
	                  'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6', 'standard', 60, 0)`)
	mustExec(`INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
	          VALUES ('chat_runboot', 'agent_runboot', 'ws_runboot', 'run boot', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`)

	// started_at uses the RFC3339 shape runAssignment writes; backdated an
	// hour so it is unambiguously pre-boot.
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, started_at, created_at)
	          VALUES ('a_runboot_orphan', 'ws_runboot', 'chat_runboot', 'agent_runboot', 'agent_runboot', 'crashed task', 'RUNNING', ?, datetime('now','-1 hour'))`, startedAt)

	queuedAt := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02 15:04:05.000")
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, queued_at, created_at)
	          VALUES ('a_runboot_queued', 'ws_runboot', 'chat_runboot', 'agent_runboot', 'agent_runboot', 'stranded behind orphan', 'QUEUED', ?, datetime('now','-30 minutes'))`, queuedAt)
	return "a_runboot_orphan", "a_runboot_queued"
}

func TestStart_RecoversOrphanedRunningAssignment_AndFreesSlot(t *testing.T) {
	// Deliberately NOT t.Parallel(): shortens the package-level sweeper
	// cadence vars (shared with the queue sweeper boot test pattern) so
	// the stranded-QUEUED promotion can also ride the fast tick if the
	// pump path is what ends up moving it.
	origInterval, origStale := stuckQueueSweepInterval, stuckQueueStaleAfter
	stuckQueueSweepInterval, stuckQueueStaleAfter = 50*time.Millisecond, 1*time.Millisecond
	t.Cleanup(func() {
		stuckQueueSweepInterval, stuckQueueStaleAfter = origInterval, origStale
	})

	db := openTestDB(t)
	orphanID, queuedID := seedOrphanedRunningAssignment(t, db)

	sockPath := filepath.Join("/tmp", "cs-runrec-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0

	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: db})
	t.Cleanup(s.StopBackground)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Observable #1: the orphaned RUNNING row must reach a terminal
	// FAILED state with a restart-recovery reason.
	// Observable #2: the QUEUED row stranded behind it must leave QUEUED
	// — proof the leaked slot was actually freed and pumped.
	deadline := time.Now().Add(30 * time.Second)
	var orphanStatus, queuedStatus string
	var errMsg sql.NullString
	recovered := false
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT status, error_message FROM assignments WHERE id = ?`, orphanID).Scan(&orphanStatus, &errMsg); err != nil {
			t.Fatalf("poll orphan status: %v", err)
		}
		if err := db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, queuedID).Scan(&queuedStatus); err != nil {
			t.Fatalf("poll queued status: %v", err)
		}
		if orphanStatus == "FAILED" && queuedStatus != "QUEUED" {
			recovered = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not shut down within 10s after ctx cancel")
	}

	if !recovered {
		t.Fatalf("orphaned RUNNING assignment not recovered after boot: orphan=%q queued=%q — Server.Start leaks the crew slot", orphanStatus, queuedStatus)
	}
	if !errMsg.Valid || errMsg.String == "" {
		t.Errorf("recovered assignment has no error_message; want a clear restart-recovery reason")
	}
}
