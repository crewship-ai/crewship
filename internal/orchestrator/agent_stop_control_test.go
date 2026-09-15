package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestStopAgent_PreparingClosesCreationGate(t *testing.T) {
	o := New(nil, nil, slog.Default())
	req := AgentRunRequest{AgentID: "a", RunID: "preparing"}
	ctx, finish := o.trackAgentRun(context.Background(), &req)
	go func() { <-ctx.Done(); finish() }()
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.StopAgent(stopCtx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := req.ExecGate(context.Background()); err == nil {
		t.Fatal("stopped preparation can still create a process")
	}
}

func TestStopAgent_CanCancelWhilePriorGateWaits(t *testing.T) {
	o := New(nil, nil, slog.Default())
	entered := make(chan struct{})
	req := AgentRunRequest{AgentID: "a", RunID: "gate-waits", ExecGate: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	ctx, finish := o.trackAgentRun(context.Background(), &req)
	go func() { _ = req.ExecGate(ctx); finish() }()
	<-entered
	bounded, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- o.StopAgent(bounded, "a") }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-bounded.Done():
		t.Fatal("stop blocked behind a caller's gate")
	}
}
