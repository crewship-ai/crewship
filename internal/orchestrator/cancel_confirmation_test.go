package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestCancellationStopRequiresBothExactRuntimeAndExecProof(t *testing.T) {
	for _, tc := range []struct {
		name, stop string
		running    bool
		inspectErr error
		execID     string
		want       bool
	}{
		{"confirmed", "ABSENT", false, nil, "agent-exec", true},
		{"wrapper-still-running", "ABSENT", true, nil, "agent-exec", false},
		{"inspect-unreadable", "ABSENT", false, errors.New("unavailable"), "agent-exec", false},
		{"session-still-present", "PRESENT", false, nil, "agent-exec", false},
		{"unknown-probe", "", false, nil, "agent-exec", false},
		{"missing-exec-id", "ABSENT", false, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := covNewRunContainer(covRunOpts{tmuxStopOut: tc.stop, agentRunning: tc.running, agentInspectErr: tc.inspectErr})
			o := New(c, newMemState(), covQuietLogger())
			location := RunLocation{ContainerID: "container-original", AgentSlug: "original", RunID: "run-original"}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			stopped, _, err := o.confirmLegacyRunStop(ctx, location, tc.execID)
			if stopped != tc.want || stopped && err != nil {
				t.Fatalf("stopped=%v err=%v", stopped, err)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, call := range c.calls {
				script := strings.Join(call.Cmd, " ")
				if call.ContainerID != "container-original" || !strings.Contains(script, TmuxSessionName("original", "run-original")) || strings.Contains(script, TmuxSessionName("original", "run-replacement")) {
					t.Fatalf("stop escaped exact runtime: %+v", call)
				}
			}
		})
	}
}

func TestRunAgentCancellationKeepsOwnershipWhenHostStopIsUnconfirmed(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n"}) // no authoritative stop reply
	state := newMemState()
	o := New(c, state, covQuietLogger())
	o.ipcToken = "fixture-key"
	o.SetDetachedExecMonitoring(time.Millisecond, time.Hour)
	req := covRunReq()
	req.RunID = "cancel-unconfirmed-original"
	req.Credentials = []Credential{{Type: "SECRET", EnvVarName: "QA_TOKEN", PlainValue: "synthetic"}}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	req.OnExecStarted = func(string) { cancel(ws.ErrExplicitStopRequested) }
	err := o.RunAgent(ctx, req, nil)
	if !errors.Is(err, ErrDetachedStillRunning) || errors.Is(err, ErrExecutionStopConfirmed) {
		t.Fatalf("unconfirmed cancellation misreported: %v", err)
	}
	if got := covRunStatus(t, state, req.RunID); got != "running" {
		t.Fatalf("unconfirmed run became terminal %s", got)
	}
	if _, _, found := runHomeLocation(req.RunID); !found {
		t.Fatal("unconfirmed cancellation removed HOME")
	}
	if len(o.runSem) != 1 || o.secretsHoldCount(req.ContainerID, req.AgentSlug, req.RunID) != 1 {
		t.Fatal("unconfirmed cancellation released capacity/credentials")
	}
	o.detachedMu.Lock()
	h := o.detached[req.RunID]
	o.detachedMu.Unlock()
	if h == nil || !h.requireExecTerminal {
		t.Fatal("exact exec watcher missing")
	}
	// This fixture never launched a process. Retire its synthetic hold so the
	// test cannot leave supervision or global HOME entries behind.
	t.Cleanup(func() { o.closeDetachedHold(h, true) })
	if err := o.RunAgent(t.Context(), req, nil); !errors.Is(err, ErrAgentDetachedBusy) {
		t.Fatalf("unconfirmed execution allowed replacement: %v", err)
	}
}

func TestRunAgentCancellationDoesNotConfirmNaturalLegacyExit(t *testing.T) {
	for _, code := range []int{0, 1, 143} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: code, tmuxStopOut: "ABSENT"})
			state := newMemState()
			o := New(c, state, covQuietLogger())
			req := covRunReq()
			req.RunID = "legacy-cancel-exit-" + fmt.Sprint(code)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			req.OnExecStarted = func(string) { cancel(ws.ErrExplicitStopRequested) }
			err := o.RunAgent(ctx, req, nil)
			if got := errors.Is(err, ErrExecutionStopConfirmed); got != (code == 143) {
				t.Fatalf("exit%d cancellation=%v err=%v", code, got, err)
			}
			wantStatus := map[int]string{0: "completed", 1: "error", 143: "cancelled"}[code]
			if got := covRunStatus(t, state, req.RunID); got != wantStatus {
				t.Fatalf("exit%d persisted %s want %s", code, got, wantStatus)
			}
			if _, held := o.detachedHoldRunID(req.AgentID); held {
				t.Fatal("terminated exact runtime retained hold")
			}
		})
	}
}

// confirmedCancellationFixture is an execution-aware provider fixture: the
// cancelled context alone cannot manufacture runtime termination evidence.
type confirmedCancellationFixture struct {
	*mockContainer
	cancel       context.CancelFunc
	stopObserved atomic.Bool
}

func newConfirmedCancellationFixture(cancel context.CancelFunc) *confirmedCancellationFixture {
	return &confirmedCancellationFixture{mockContainer: &mockContainer{}, cancel: cancel}
}
func (c *confirmedCancellationFixture) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) > 0 {
		c.cancel()
		return &provider.ExecResult{ExecID: "cancelled-original-exec", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	if strings.Contains(strings.Join(cfg.Cmd, " "), "tmux kill-session") {
		c.stopObserved.Store(true)
		return &provider.ExecResult{ExecID: "original-stop-probe", Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (c *confirmedCancellationFixture) ExecInspect(_ context.Context, id string) (bool, int, error) {
	if id == "cancelled-original-exec" {
		if c.stopObserved.Load() {
			return false, 143, nil
		}
		return true, 0, nil
	}
	return false, 0, nil
}

// The legacy (non-host-capture) cancellation branch records the stopped run
// as cancelled whatever ended its context, but only the chat's explicit stop
// request — not a hub/server shutdown, a plain cancel, or a concurrent agent
// stop — may be confirmed to the requester.
func TestRunAgentLegacyCancellationConfirmsOnlyExplicitStop(t *testing.T) {
	shutdown := errors.New("hub shutting down")
	for _, tc := range []struct {
		name      string
		cause     error
		agentStop bool
		confirmed bool
	}{
		{"explicit-stop", ws.ErrExplicitStopRequested, false, true},
		{"plain-cancel", nil, false, false},
		{"base-context-shutdown", shutdown, false, false},
		{"explicit-stop-racing-agent-stop", ws.ErrExplicitStopRequested, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: 143, tmuxStopOut: "ABSENT"})
			state := newMemState()
			o := New(c, state, covQuietLogger())
			req := covRunReq()
			req.RunID = "legacy-cause-" + tc.name
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			req.OnExecStarted = func(string) {
				if tc.agentStop {
					v, ok := o.agentRuns.Load(req.RunID)
					if !ok {
						t.Error("run control not registered")
					} else {
						rc := v.(*agentRunControl)
						rc.mu.Lock()
						rc.stopped = true
						rc.mu.Unlock()
					}
				}
				cancel(tc.cause)
			}
			err := o.RunAgent(ctx, req, nil)
			if got := errors.Is(err, ErrExecutionStopConfirmed); got != tc.confirmed {
				t.Fatalf("confirmed=%v want %v err=%v", got, tc.confirmed, err)
			}
			if err == nil {
				t.Fatal("cancellation not reported")
			}
			if got := covRunStatus(t, state, req.RunID); got != "cancelled" {
				t.Fatalf("persisted %s want cancelled", got)
			}
		})
	}
}

// An agent stop (exit 143 with the agent's stop flag) is recorded as
// cancelled but is never a confirmation of a chat cancellation request.
func TestRunAgentAgentStopIsNotChatCancellationConfirmation(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: 143})
	state := newMemState()
	o := New(c, state, covQuietLogger())
	req := covRunReq()
	req.RunID = "agent-stop-not-chat-confirmation"
	req.OnExecStarted = func(string) {
		v, ok := o.agentRuns.Load(req.RunID)
		if !ok {
			t.Error("run control not registered")
			return
		}
		rc := v.(*agentRunControl)
		rc.mu.Lock()
		rc.stopped = true
		rc.mu.Unlock()
	}
	err := o.RunAgent(t.Context(), req, nil)
	if !errors.Is(err, ErrAgentStopped) || errors.Is(err, ErrExecutionStopConfirmed) {
		t.Fatalf("agent stop outcome misreported: %v", err)
	}
}

// absentableContainer reports its container gone (and its execs
// uninspectable) once absent is set, as when an operator removes it.
type absentableContainer struct {
	*covContainer
	absent atomic.Bool
}

func (c *absentableContainer) ContainerStatus(ctx context.Context, id string) (*provider.ContainerStatus, error) {
	if c.absent.Load() {
		return nil, provider.ErrContainerNotFound
	}
	return c.covContainer.ContainerStatus(ctx, id)
}

func (c *absentableContainer) ExecInspect(ctx context.Context, id string) (bool, int, error) {
	if c.absent.Load() {
		return false, 0, errors.New("no such exec")
	}
	return c.covContainer.ExecInspect(ctx, id)
}

// B12 (G4): a legacy run whose container is removed while its stop
// confirmation is pending must release its holds, not retry forever.
func TestRunAgentCancellationReleasesHoldsWhenContainerIsRemoved(t *testing.T) {
	c := &absentableContainer{covContainer: covNewRunContainer(covRunOpts{stream: "{}\n"})} // no authoritative stop reply
	o := New(c, newMemState(), covQuietLogger())
	o.ipcToken = "fixture-key"
	o.SetDetachedExecMonitoring(time.Millisecond, time.Millisecond) // the watcher must tick
	req := covRunReq()
	req.RunID = "cancel-container-removed"
	req.Credentials = []Credential{{Type: "SECRET", EnvVarName: "QA_TOKEN", PlainValue: "synthetic"}}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	req.OnExecStarted = func(string) { cancel(ws.ErrExplicitStopRequested) }
	if err := o.RunAgent(ctx, req, nil); !errors.Is(err, ErrDetachedStillRunning) {
		t.Fatalf("unconfirmed cancellation misreported: %v", err)
	}
	o.detachedMu.Lock()
	h := o.detached[req.RunID]
	o.detachedMu.Unlock()
	if h == nil || !h.requireExecTerminal {
		t.Fatal("exact exec watcher missing")
	}
	t.Cleanup(func() { o.closeDetachedHold(h, true) })
	c.absent.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for {
		o.detachedMu.Lock()
		_, held := o.detached[req.RunID]
		o.detachedMu.Unlock()
		if !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hold survived the container's removal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(o.runSem) != 0 || o.secretsHoldCount(req.ContainerID, req.AgentSlug, req.RunID) != 0 {
		t.Fatal("removed container left capacity or credential holds")
	}
	if _, _, found := runHomeLocation(req.RunID); found {
		t.Fatal("removed container left its HOME registration")
	}
}
