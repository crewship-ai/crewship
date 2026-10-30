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
