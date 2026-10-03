package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type reviewContainer struct {
	provider.ContainerProvider
	state      string
	inspectErr error
}

func (c reviewContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("exec unavailable")
}
func (c reviewContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: c.state}, c.inspectErr
}
func TestReviewStopMissingOrStoppedContainer(t *testing.T) {
	for _, state := range []string{"missing", "stopped", "running", "creating", "unreachable"} {
		t.Run(state, func(t *testing.T) {
			store := newMemState()
			run := RunState{ID: "review-stop", AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "running"}
			raw, _ := json.Marshal(run)
			_ = store.Set(t.Context(), "agent_runs", run.ID, raw)
			c := reviewContainer{state: state}
			if state == "missing" {
				c.inspectErr = provider.ErrContainerNotFound
			}
			if state == "unreachable" {
				c.inspectErr = errors.New("daemon unavailable")
			}
			o := New(c, store, slog.Default())
			err := o.StopAgent(t.Context(), "a")
			if state == "stopped" || state == "missing" {
				raw, readErr := store.Get(t.Context(), "agent_runs", run.ID)
				var persisted RunState
				if readErr != nil {
					t.Fatal(readErr)
				}
				if decodeErr := json.Unmarshal(raw, &persisted); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if err != nil || persisted.Status != "cancelled" {
					t.Fatalf("stopped container stuck: %v", err)
				}
			} else if err == nil {
				t.Fatal("uncertain runtime reported stopped")
			}
		})
	}
}

type reviewAbsentContainer struct{ provider.ContainerProvider }

func (reviewAbsentContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
}
func TestReviewStopFindsOwnerRegisteredDuringStateList(t *testing.T) {
	store := newMemState()
	var o *Orchestrator
	var req AgentRunRequest
	var runCtx context.Context
	state := stopReviewState{StateProvider: store, list: func(ctx context.Context) (map[string][]byte, error) {
		req = AgentRunRequest{RunID: "late-owner", AgentID: "a", AgentSlug: "a", ContainerID: "c"}
		var finish func()
		runCtx, finish = o.trackAgentRun(ctx, &req)
		go func() { <-runCtx.Done(); finish() }()
		raw, _ := json.Marshal(RunState{ID: req.RunID, AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "running"})
		_ = store.Set(ctx, "agent_runs", req.RunID, raw)
		return store.List(ctx, "agent_runs")
	}}
	o = New(reviewAbsentContainer{}, state, slog.Default())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := o.StopAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := req.ExecGate(context.Background()); err == nil {
		t.Fatal("STOPPED returned with live creation gate")
	}
}
