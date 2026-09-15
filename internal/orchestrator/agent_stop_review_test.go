package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type stopReviewState struct {
	provider.StateProvider
	list func(context.Context) (map[string][]byte, error)
}

func (s stopReviewState) List(ctx context.Context, _ string) (map[string][]byte, error) {
	return s.list(ctx)
}

func TestStopAgent_StateFailureStillCancelsOwnedPreparation(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "lookup_error", true: "malformed_record"}[broken], func(t *testing.T) {
			state := stopReviewState{list: func(context.Context) (map[string][]byte, error) {
				if broken {
					return map[string][]byte{"bad": []byte("invalid")}, nil
				}
				return nil, errors.New("unavailable")
			}}
			o := New(nil, state, slog.Default())
			req := AgentRunRequest{AgentID: "a", RunID: "preparation"}
			ctx, finish := o.trackAgentRun(context.Background(), &req)
			defer finish()
			bounded, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := o.StopAgent(bounded, "a"); err == nil {
				t.Fatal("bad ownership must not confirm stop")
			}
			if ctx.Err() == nil {
				t.Fatal("ownership failure left preparation able to create a process")
			}
		})
	}
}

type stopReviewContainer struct {
	provider.ContainerProvider
	seen    chan struct{}
	release chan struct{}
}

func (c stopReviewContainer) Exec(ctx context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	c.seen <- struct{}{}
	select {
	case <-c.release:
		return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestStopAgent_AllOwnedRuntimesReceiveStopBeforeWaiting(t *testing.T) {
	c := stopReviewContainer{seen: make(chan struct{}, 4), release: make(chan struct{})}
	o := New(c, nil, slog.Default())
	for _, id := range []string{"one", "two"} {
		req := AgentRunRequest{AgentID: "a", AgentSlug: "a", ContainerID: "c", RunID: id}
		_, finish := o.trackAgentRun(context.Background(), &req)
		defer finish()
		if err := req.ExecGate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.StopAgent(ctx, "a") }()
	for range 2 {
		select {
		case <-c.seen:
		case <-ctx.Done():
			t.Fatal("one runtime's stop prevented the other runtime receiving its stop")
		}
	}
	// No invocation settlement is fabricated: stop must remain unconfirmed.
	cancel()
	close(c.release)
	if err := <-done; err == nil {
		t.Fatal("unsettled invocations were reported stopped")
	}
}
