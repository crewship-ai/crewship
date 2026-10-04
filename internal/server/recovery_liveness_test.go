package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

type recoveredProbeContainer struct {
	*mockContainer
	answer          string
	err             error
	state, identity string
}

func (c *recoveredProbeContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	state := c.state
	if state == "" {
		state = "running"
	}
	if c.identity != "" {
		id = c.identity
	}
	return &provider.ContainerStatus{ID: id, State: state}, nil
}

func (c *recoveredProbeContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &provider.ExecResult{ExecID: "probe", Reader: io.NopCloser(strings.NewReader(c.answer))}, nil
}

func TestRecoveryChecksPersistedRuntimeLiveness(t *testing.T) {
	for _, tc := range []struct {
		name, answer    string
		err             error
		terminal        bool
		state, identity string
	}{
		{name: "gone", answer: "ABSENT", terminal: true},
		{name: "alive", answer: "PRESENT"},
		{name: "unreadable", err: errors.New("runtime unavailable")},
		{name: "stopped-container", state: "stopped", err: errors.New("must not exec a stopped container"), terminal: true},
		{name: "unknown-container-state", state: "creating", answer: "ABSENT"},
		{name: "different-container", identity: "replacement-runtime", answer: "ABSENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			c := &recoveredProbeContainer{mockContainer: &mockContainer{}, answer: tc.answer, err: tc.err, state: tc.state, identity: tc.identity}
			s.container = c
			s.orchestrator = orchestrator.New(c, s.state, s.logger)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			seedRecoveryTrace(t, s, "recovered-run", "a")
			raw, err := json.Marshal(orchestrator.RunState{ID: "recovered-run", AgentID: "a", AgentSlug: "a", ContainerID: "runtime", Status: "running"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.state.Set(t.Context(), "agent_runs", "recovered-run", raw); err != nil {
				t.Fatal(err)
			}
			s.recoverOrphanedRuns(t.Context())
			n := recoveryTerminalCount(t, s, "recovered-run")
			if (n == 1) != tc.terminal {
				t.Fatalf("terminal records=%d want confirmed absence=%v", n, tc.terminal)
			}
			raw, err = s.state.Get(t.Context(), "agent_runs", "recovered-run")
			if err != nil {
				t.Fatal(err)
			}
			var run orchestrator.RunState
			if err := json.Unmarshal(raw, &run); err != nil {
				t.Fatal(err)
			}
			if (run.Status != "running") != tc.terminal {
				t.Fatalf("durable runtime status=%q", run.Status)
			}
		})
	}
}

func TestRecoveryFollowsSurvivorWithoutTouchingNewRuns(t *testing.T) {
	s := newTestServerWithDeps(t)
	c := &recoveredProbeContainer{mockContainer: &mockContainer{}, answer: "PRESENT"}
	s.container = c
	s.orchestrator = orchestrator.New(c, s.state, s.logger)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	put := func(id string) {
		t.Helper()
		seedRecoveryTrace(t, s, id, "a")
		raw, err := json.Marshal(orchestrator.RunState{ID: id, AgentID: "a", AgentSlug: "a", ContainerID: "runtime", Status: "running"})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.state.Set(t.Context(), "agent_runs", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	put("survivor")
	s.recoverOrphanedRuns(t.Context())
	put("new-admission")
	c.err = errors.New("temporarily unavailable")
	s.reconcileRecoveredRuntimes(t.Context())
	if got := recoveryTerminalCount(t, s, "survivor"); got != 0 {
		t.Fatalf("unknown runtime cancelled: %d", got)
	}
	c.err = nil
	c.answer = "ABSENT"
	s.reconcileRecoveredRuntimes(t.Context())
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := recoveryTerminalCount(t, s, "survivor"); got != 1 {
		t.Fatalf("survivor terminal count=%d, want 1", got)
	}
	if got := recoveryTerminalCount(t, s, "new-admission"); got != 0 {
		t.Fatalf("new admission was cancelled: %d", got)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("new admission agent status=%s", status)
	}
	if pendingStop(t, s, "survivor") {
		t.Fatal("terminal publication left its outbox pending")
	}
	var reason string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.reason') FROM journal_entries WHERE id='recovered-stop:survivor'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "recovered_runtime_absent" {
		t.Fatalf("wrong recovery reason %q", reason)
	}
	// A repeated probe must not duplicate the terminal event.
	s.reconcileRecoveredRuntimes(t.Context())
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := recoveryTerminalCount(t, s, "survivor"); got != 1 {
		t.Fatalf("duplicate terminal: %d", got)
	}
}
