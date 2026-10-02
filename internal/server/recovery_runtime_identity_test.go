package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

func TestRecoveryPreservesDurableRunningAgent(t *testing.T) {
	s := newTestServerWithDeps(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s.state = state
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('recover-w','Recovery','recovery')`)
	for _, id := range []string{"live", "orphan"} {
		mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES(?,'recover-w',?,?,'RUNNING')`, id, id, id)
		mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES(?,'recover-w',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.started','info','sidecar','started','{}','{}',?,'normal')`, "j-"+id, id, "trace-"+id)
	}
	raw, err := json.Marshal(orchestrator.RunState{ID: "runtime-live", AgentID: "live", AgentSlug: "live", ContainerID: "container-live", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", "runtime-live", raw); err != nil {
		t.Fatal(err)
	}
	s.recoverOrphanedRuns(t.Context())
	if err := s.journalWriter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='live'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("unconfirmed runtime reported %s", status)
	}
	var canceled int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE agent_id='live' AND entry_type='run.cancelled'`).Scan(&canceled); err != nil {
		t.Fatal(err)
	}
	if canceled != 0 {
		t.Fatal("recovery invented process cancellation")
	}
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='orphan'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("journal-only orphan was not recovered: %s", status)
	}
}
