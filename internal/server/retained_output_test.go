package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/runoutput"
	"github.com/crewship-ai/crewship/internal/work"
)

func seedHostOutput(t *testing.T, s *Server, runID string, output ...string) orchestrator.RunState {
	t.Helper()
	if uid := os.Geteuid(); os.Getenv("CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED") == "1" && (uid == 0 || uid == 1001 || uid == 1002) {
		t.Fatal("required capture qualification needs distinct unprivileged host UID")
	}
	if uid := os.Geteuid(); uid == 1001 || uid == 1002 {
		// SKIP-WAIVER(#3055): workload identities cannot own protected spools;
		// every guarded fixture runs in the required distinct-UID lane.
		t.Skip("protected capture requires host UID distinct from workload/sidecar; required qualification uses distinct unprivileged UID")
	}
	// A private sibling under sticky /tmp also works inside the resource
	// wrapper, whose build TMPDIR has deliberately shared ancestors.
	root, err := os.MkdirTemp("/tmp", "crewship-retained-server-")
	if err != nil {
		t.Fatal(err)
	}
	createdRoot := root
	t.Cleanup(func() { _ = os.RemoveAll(createdRoot) })
	// Darwin's /tmp is a system alias. Admit the real protected directory.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	s.orchestrator.SetHostOutputRoot(root)
	directory := filepath.Join(root, runID)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	identity, err := json.Marshal(runoutput.HostIdentity{Version: 1, ContainerID: "runtime", ExecID: "actual-exec", Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "identity.json"), identity, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := runoutput.Create(filepath.Join(directory, "output"), runoutput.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	text := "first\nsecond"
	if len(output) > 0 {
		text = output[0]
	}
	if _, err := store.Write("stdout", []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	run := orchestrator.RunState{ID: runID, AgentID: "retained-agent", AgentSlug: "retained", WorkspaceID: "retained-ws", ContainerID: "runtime", ExecID: "actual-exec", Status: "running", StartedAt: time.Now().UTC(), Output: &orchestrator.RunOutputState{Version: 2, Directory: filepath.Join(directory, "output"), Deadline: deadline, Adapter: "fixture-command"}}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	return run
}

func retainedWorkFixture(t *testing.T, s *Server, workspace string) (*work.Store, *work.Claimed) {
	t.Helper()
	store := work.NewStore(s.db)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: workspace, AgentID: "retained-agent", Source: work.SourceWebhook, Class: work.ClassBackground}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "departed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(t.Context(), work.TransitionRequest{WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: work.StateNeedsReconciliation, Reason: "protected host capture awaiting retained result"}); err != nil {
		t.Fatal(err)
	}
	return store, a
}

func TestRetainedOutputCannotSettleForeignWork(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	store, a := retainedWorkFixture(t, s, "foreign-workspace")
	run := seedHostOutput(t, s, a.RunID)
	if err := s.projectRetainedOutput(t.Context(), run); err == nil {
		t.Fatal("foreign work settled")
	}
	item, err := store.Get(t.Context(), a.Item.ID)
	if err != nil || item.State != work.StateNeedsReconciliation {
		t.Fatalf("foreign work changed: %+v %v", item, err)
	}
	p, _, err := store.RunProjection(t.Context(), a.RunID)
	if err != nil || p.Ready {
		t.Fatalf("foreign result staged: %+v %v", p, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=?`, a.RunID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("foreign projected rows=%d", n)
	}
}

func TestRetainedOutputZeroExitInBandFailureCannotSucceed(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	store, a := retainedWorkFixture(t, s, "retained-ws")
	run := seedHostOutput(t, s, a.RunID, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"synthetic failure"}`+"\n")
	run.Output.Adapter = "CLAUDE_CODE"
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(t.Context(), a.Item.ID)
	if err != nil || item.State != work.StateNeedsReconciliation {
		t.Fatalf("in-band failure succeeded: %+v %v", item, err)
	}
	p, _, err := store.RunProjection(t.Context(), a.RunID)
	if err != nil || !p.Ready || p.Status != "FAILED" || p.Result.Metadata["in_band_error"] != true {
		t.Fatalf("wrong result: %+v %v", p, err)
	}
}

func TestRetainedOutputReplaysAcrossAcknowledgementFailure(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	run := seedHostOutput(t, s, "retained-replay")
	// The first line commits, then the next journal write fails. On retry,
	// only unacknowledged deterministic keys are projected.
	mustExec(t, s.db, `CREATE TRIGGER fail_retained BEFORE INSERT ON journal_entries WHEN NEW.id='retained:retained-replay:3:0' BEGIN SELECT RAISE(FAIL,'journal unavailable'); END`)
	if err := s.projectRetainedOutput(t.Context(), run); err == nil {
		t.Fatal("failed journal write acknowledged")
	}
	mustExec(t, s.db, `DROP TRIGGER fail_retained`)
	if err := s.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := s.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	var lines, terminals int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='exec.output_chunk'`, run.ID).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.completed'`, run.ID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if lines != 2 || terminals != 1 {
		t.Fatalf("lines=%d terminals=%d", lines, terminals)
	}
}

func TestRetainedOutputRefusesForeignOwnership(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	run := seedHostOutput(t, s, "retained-foreign")
	run.WorkspaceID = "foreign"
	if err := s.projectRetainedOutput(t.Context(), run); err == nil {
		t.Fatal("foreign output projected")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=?`, run.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("foreign rows=%d", n)
	}
}

// Source ACK loss after ordinary history pruning must not resurrect a terminal.
func TestRetainedOutputTerminalReceiptSurvivesPruningAndLostACK(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	run := seedHostOutput(t, s, "retained-pruned")
	if err := s.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.db, `DELETE FROM journal_entries WHERE trace_id='retained-pruned'`)
	if err := os.Remove(filepath.Join(filepath.Dir(run.Output.Directory), "replay-ack.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=?`, run.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pruned history resurrected: %d entries", n)
	}
}

func TestRetainedOutputCannotSettleAnotherAgentWork(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	_, a := retainedWorkFixture(t, s, "retained-ws")
	mustExec(t, s.db, `UPDATE work_items SET agent_id='other-agent' WHERE id=?`, a.Item.ID)
	run := seedHostOutput(t, s, a.RunID)
	if err := s.projectRetainedOutput(t.Context(), run); err == nil {
		t.Fatal("another agent work settled")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=?`, a.RunID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("foreign projected rows=%d", n)
	}
}

func TestRetainedOutputRefusesConflictingExitAfterTerminalPruning(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	run := seedHostOutput(t, s, "retained-conflicting-exit")
	contradictory := 9
	entry := journal.RunTerminalReplay(run.ID, run.WorkspaceID, run.AgentID, run.StartedAt, journal.EntryRunCompleted, false, &contradictory, nil, nil)
	if _, _, err := s.journalWriter.EmitReplaySync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.db, `DELETE FROM journal_entries WHERE trace_id=?`, run.ID)
	if err := s.projectRetainedOutput(t.Context(), run); !errors.Is(err, journal.ErrReplayConflict) {
		t.Fatalf("contradictory exit acknowledged: %v", err)
	}
	raw, err := s.state.Get(t.Context(), "agent_runs", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var current orchestrator.RunState
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" || current.Output.Acknowledged {
		t.Fatalf("conflicting source settled: %+v", current)
	}
}
