package orchestrator

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestPrivateDetachedHoldRequiresActualExecDeath(t *testing.T) {
	c := covNewRunContainer(covRunOpts{})
	// There is no tmux session for this direct exec. Its absence says
	// nothing about the process created by the private runtime.
	c.route = func(provider.ExecConfig) (*provider.ExecResult, error) {
		return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT\n"))}, nil
	}
	probed := make(chan struct{}, 1)
	allowProbe := make(chan struct{})
	var gone atomic.Bool
	c.inspect = func(id string) (bool, int, error) {
		if id != "private-exec" {
			t.Errorf("inspected another process %q", id)
		}
		running := !gone.Load()
		select {
		case probed <- struct{}{}:
		default:
		}
		<-allowProbe
		return running, 0, nil
	}
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq().WithStrictContentPrivacy()
	releaseAgent, err := o.acquireAgentAdmissionMode(context.Background(), req.AgentID, true)
	if err != nil {
		t.Fatal(err)
	}
	releaseServer, err := o.acquireServerAdmission(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	var released atomic.Int32
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "private-exec", req: req, release: func() { released.Add(1); releaseServer(); releaseAgent() }, startedAt: time.Now()}
	if err := o.registerDetachedHold(h, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gone.Store(true)
		select {
		case <-allowProbe:
		default:
			close(allowProbe)
		}
	})
	select {
	case <-probed:
	case <-h.done:
		t.Fatal("absent tmux session released a live private exec")
	case <-time.After(5 * time.Second):
		t.Fatal("actual private exec was never inspected")
	}
	if released.Load() != 0 {
		t.Fatal("released before actual exec termination")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); !held {
		t.Fatal("lost original agent admission")
	}
	if release, err := o.acquireAgentAdmissionMode(context.Background(), req.AgentID, true); err == nil {
		release()
		t.Fatal("second run entered while original process remained alive")
	}
	gone.Store(true)
	close(allowProbe)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed process death did not release original hold")
	}
	if released.Load() != 1 {
		t.Fatal("capacity release must occur exactly once")
	}
}
