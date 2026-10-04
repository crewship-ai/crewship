package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

type reviewRecoveryContainer struct {
	provider.ContainerProvider
	state string
	err   error
}

func (c reviewRecoveryContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: c.state}, c.err
}

func (c reviewRecoveryContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("runtime probe unavailable")
}
func TestReviewRestartReconcilesAbsentContainer(t *testing.T) {
	for _, kind := range []string{"stopped", "error", "missing", "running", "unreachable"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('review','Review','review')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','review','Agent','a','RUNNING')`)
			// No journal trace: manual IPC runs must also leave RUNNING after confirmed absence.
			raw, _ := json.Marshal(orchestrator.RunState{ID: "review-run", AgentID: "a", ContainerID: "old-container", AgentSlug: "a", Status: "running"})
			if err := s.state.Set(t.Context(), "agent_runs", "review-run", raw); err != nil {
				t.Fatal(err)
			}
			c := reviewRecoveryContainer{state: kind}
			if kind == "missing" {
				c.err = provider.ErrContainerNotFound
			}
			if kind == "unreachable" {
				c.err = errors.New("daemon offline")
			}
			s.container = c
			s.orchestrator = orchestrator.New(c, s.state, s.logger)
			s.recoverOrphanedRuns(t.Context())
			var status string
			if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "RUNNING"
			if kind == "stopped" || kind == "error" || kind == "missing" {
				want = "IDLE"
			}
			if status != want {
				t.Fatalf("status=%s want %s", status, want)
			}
			raw, err := s.state.Get(t.Context(), "agent_runs", "review-run")
			if err != nil {
				t.Fatal(err)
			}
			var run orchestrator.RunState
			if err := json.Unmarshal(raw, &run); err != nil {
				t.Fatal(err)
			}
			if want == "IDLE" && run.Status != "cancelled" {
				t.Fatalf("stale runtime: %s", run.Status)
			}
		})
	}
}
func TestReviewStatusScopesCorruptRecord(t *testing.T) {
	s := newTestServerWithDeps(t)
	if err := s.state.Set(t.Context(), "agent_runs", "broken", []byte(`{"agent_id":"b","started_at":42}`)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		out := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(out, httptest.NewRequest("GET", "/agents/"+id+"/status", nil))
		want := 200
		if id == "b" {
			want = 503
		}
		if out.Code != want {
			t.Fatalf("agent=%s status=%d want %d", id, out.Code, want)
		}
	}
}
