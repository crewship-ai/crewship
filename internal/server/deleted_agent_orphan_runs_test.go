package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func seedAgedTrace(t *testing.T, s *Server, id, agent, age string) {
	t.Helper()
	mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority)
 VALUES(?,'rw',?,strftime('%Y-%m-%dT%H:%M:%fZ','now',?),'run.started','info','sidecar','started','{}','{}',?,'normal')`, "j-"+id, agent, age, id)
}

func setRuntimeRecord(t *testing.T, s *Server, key string, raw []byte) {
	t.Helper()
	if err := s.state.Set(t.Context(), "agent_runs", key, raw); err != nil {
		t.Fatal(err)
	}
}

// #5511: runs of deleted agents stayed RUNNING until the next server restart
// (and forever if it never came). The periodic reaper ends exactly those no
// runtime can still execute, and nothing else.
func TestReapDeletedAgentOrphanRuns(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, s *Server)
		wantEnds bool
	}{
		{name: "soft-deleted agent, no runtime record", wantEnds: true, setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
		}},
		{name: "agent row gone", wantEnds: true, setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `DELETE FROM agents WHERE id='a'`)
		}},
		{name: "deleted agent whose runtime was confirmed absent", wantEnds: true, setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "other", AgentID: "a", Status: "cancelled"})
			setRuntimeRecord(t, s, "other", raw)
		}},
		{name: "live agent", setup: func(t *testing.T, s *Server) {}},
		{name: "deleted agent with a running runtime record", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "other", AgentID: "a", ContainerID: "c", AgentSlug: "a", Status: "running"})
			setRuntimeRecord(t, s, "other", raw)
		}},
		{name: "deleted agent with a legacy agent-keyed record", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "a", AgentID: "a", Status: "running"})
			setRuntimeRecord(t, s, "a", raw)
		}},
		{name: "deleted agent with a pending stop projection", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "other", AgentID: "a", WorkspaceID: "rw", Status: "cancelled", StopJournalPending: true})
			setRuntimeRecord(t, s, "other", raw)
		}},
		// Review S2: retained output already terminal in the runtime record but
		// not yet projected to the journal must be left to its own outcome.
		{name: "deleted agent with unprojected retained output", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "orphan", AgentID: "a", Status: "completed", Output: &orchestrator.RunOutputState{Version: 2}})
			setRuntimeRecord(t, s, "orphan", raw)
		}},
		{name: "deleted agent whose retained output was projected", wantEnds: true, setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			raw, _ := json.Marshal(orchestrator.RunState{ID: "other", AgentID: "a", Status: "completed", Output: &orchestrator.RunOutputState{Version: 2, Acknowledged: true}})
			setRuntimeRecord(t, s, "other", raw)
		}},
		{name: "deleted agent with durable work ownership", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			mustExec(t, s.db, `INSERT INTO work_items(id,workspace_id,source,state,eligible_at,created_at,updated_at) VALUES('w','rw','manual','running','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
			mustExec(t, s.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES('orphan','w',1,1,'x','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
		}},
		{name: "deleted agent with an unreadable record", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			setRuntimeRecord(t, s, "broken", []byte(`{"agent_id":"a","status":7}`))
		}},
		{name: "fresh start of a deleted agent", setup: func(t *testing.T, s *Server) {
			mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			mustExec(t, s.db, `UPDATE journal_entries SET ts=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE trace_id='orphan'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','IDLE')`)
			seedAgedTrace(t, s, "orphan", "a", "-1 hours")
			tc.setup(t, s)

			// The passes that existed before #5511 leave the trace open.
			_ = s.flushRecoveredStops(t.Context()) // may legitimately retry a pending stop
			s.reconcileRecoveredRuntimes(t.Context())
			if got := recoveryTerminalCount(t, s, "orphan"); got != 0 {
				t.Fatalf("pre-existing passes already ended the trace (%d terminals)", got)
			}

			n, err := s.reapDeletedAgentOrphanRuns(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.wantEnds {
				want = 1
			}
			if n != want {
				t.Fatalf("reaped %d, want %d", n, want)
			}
			if got := recoveryTerminalCount(t, s, "orphan"); got != want {
				t.Fatalf("terminal entries = %d, want %d", got, want)
			}
			if tc.wantEnds {
				if kind, reason := terminalOf(t, s, "orphan"); kind != "run.cancelled" || reason != "agent_deleted" {
					t.Fatalf("terminal = %s/%s, want run.cancelled/agent_deleted", kind, reason)
				}
				// Idempotent: a second pass finds nothing.
				if n, err := s.reapDeletedAgentOrphanRuns(t.Context()); err != nil || n != 0 {
					t.Fatalf("second pass reaped %d (err %v)", n, err)
				}
			}
		})
	}
}

// Review L3: candidates blocked by an open runtime record must not keep the
// batch full for ever; the scan resumes after them.
func TestReapDeletedAgentOrphanRunsDoesNotStarve(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status,deleted_at) VALUES('blocked','rw','Blocked','blocked','IDLE',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status,deleted_at) VALUES('gone','rw','Gone','gone','IDLE',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	raw, _ := json.Marshal(orchestrator.RunState{ID: "live", AgentID: "blocked", ContainerID: "c", AgentSlug: "blocked", Status: "running"})
	setRuntimeRecord(t, s, "live", raw)
	for i := 0; i <= deletedAgentOrphanBatch; i++ {
		seedAgedTrace(t, s, fmt.Sprintf("a-%03d", i), "blocked", "-1 hours")
	}
	seedAgedTrace(t, s, "z-reapable", "gone", "-1 hours")
	reaped := 0
	for pass := 0; pass < 3; pass++ {
		n, err := s.reapDeletedAgentOrphanRuns(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		reaped += n
	}
	if reaped != 1 || recoveryTerminalCount(t, s, "z-reapable") != 1 {
		t.Fatalf("reaped %d; the reapable trace behind a full page of blocked ones was starved", reaped)
	}
	if recoveryTerminalCount(t, s, "a-000") != 0 {
		t.Fatal("blocked agent's trace was ended")
	}
}
