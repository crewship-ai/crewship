package chatbridge

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type agentStopOutcomeContainer struct {
	*scriptedContainer
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (c *agentStopOutcomeContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if strings.Contains(strings.Join(cfg.Cmd, " "), "/bin/kill -TERM") {
		c.once.Do(func() { close(c.stopped) })
		return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	}
	if len(cfg.Env) > 0 {
		r, w := io.Pipe()
		go func() {
			<-c.stopped
			output := claudeSuccessOutput(0)
			if c.exitCode != 0 {
				output = strings.Split(output, `{"type":"result"`)[0]
			}
			_, _ = io.WriteString(w, output)
			_ = w.Close()
		}()
		close(c.started)
		return &provider.ExecResult{ExecID: "agent-exec", Reader: r}, nil
	}
	return c.scriptedContainer.Exec(ctx, cfg)
}

func TestAgentStop_ChatOutcomePreservesStopAndLateSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		want string
	}{
		{"terminated", 143, "CANCELLED"},
		{"completed_before_signal", 0, "COMPLETED"},
		{"independent_failure", 1, "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &capResolver{info: baseInfo()}
			ctr := &agentStopOutcomeContainer{scriptedContainer: &scriptedContainer{exitCode: tc.exit}, started: make(chan struct{}), stopped: make(chan struct{})}
			b := testBridgeWithContainer(t, resolver, ctr)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			defer ctr.once.Do(func() { close(ctr.stopped) })
			done := make(chan error, 1)
			go func() { done <- b.HandleChatMessage(ctx, "user-1", "stop-outcome", "hi", func(ws.ChatEvent) {}) }()
			select {
			case <-ctr.started:
			case <-ctx.Done():
				t.Fatal("agent never started")
			}
			if err := b.orch.StopAgent(ctx, resolver.info.AgentID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("chat never settled")
			}
			if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != tc.want {
				t.Fatalf("run updates = %+v, want %s", resolver.runUpdates, tc.want)
			}
		})
	}
}
