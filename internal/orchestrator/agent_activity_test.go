package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestReconcileAgentActivityProtectsPreparationBeforeDurableState(t *testing.T) {
	o := New(nil, nil, slog.Default())
	req := AgentRunRequest{AgentID: "a", RunID: "preparing"}
	_, finish := o.trackAgentRun(context.Background(), &req)
	defer finish()
	for _, test := range []struct {
		agent  string
		active bool
	}{{"a", true}, {"other", false}} {
		sentinel := errors.New("projection unavailable")
		err := o.ReconcileAgentActivity(test.agent, func(active bool) error {
			if active != test.active {
				t.Fatalf("agent %s active=%v", test.agent, active)
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("projection error lost: %v", err)
		}
	}
}

func TestReconcileAgentActivitySerializesAdmissionWithProjection(t *testing.T) {
	o := New(nil, nil, slog.Default())
	entered, release, projected := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		projected <- o.ReconcileAgentActivity("a", func(active bool) error {
			if active {
				return errors.New("unexpected initial invocation")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	attempted, admitted := make(chan struct{}), make(chan func(), 1)
	go func() {
		close(attempted)
		req := AgentRunRequest{AgentID: "a", RunID: "new"}
		_, finish := o.trackAgentRun(context.Background(), &req)
		admitted <- finish
	}()
	<-attempted
	select {
	case finish := <-admitted:
		close(release)
		finish()
		<-projected
		t.Fatal("invocation admitted before status projection finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-projected; err != nil {
		t.Fatal(err)
	}
	select {
	case finish := <-admitted:
		defer finish()
	case <-time.After(time.Second):
		t.Fatal("admission did not resume after projection")
	}
	if err := o.ReconcileAgentActivity("a", func(active bool) error {
		if !active {
			return errors.New("admitted invocation not protected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAgentActivityIgnoresFinishedControlAwaitingRemoval(t *testing.T) {
	o := New(nil, nil, slog.Default())
	done := make(chan struct{})
	close(done)
	o.agentRuns.Store("finished", &agentRunControl{agentID: "a", done: done})
	if err := o.ReconcileAgentActivity("a", func(active bool) error {
		if active {
			return errors.New("finished registry entry kept agent running")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
