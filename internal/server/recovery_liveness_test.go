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
