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

type runImageContainer struct {
	execStartedProbeContainer
	status *provider.ContainerStatus
	err    error
}

type deadlineImageContainer struct {
	execStartedProbeContainer
	t *testing.T
}

func (c deadlineImageContainer) ContainerStatus(ctx context.Context, _ string) (*provider.ContainerStatus, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		c.t.Error("image observation has no bounded deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunImageObservationHonorsCancellation(t *testing.T) {
	o := New(deadlineImageContainer{t: t}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := o.observeRunImage(ctx, "c1"); got != "" {
		t.Fatalf("cancelled observation = %q", got)
	}
}

func TestExecCommandImageCannotBeReplacedByOutcome(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, phase := range []string{"start", "error", "end"} {
		for _, observed := range []string{"", imageID} {
			payload := execCommandPayload(AgentRunRequest{runtimeImageID: observed}, journalCmdView{}, phase, map[string]any{"runtime_image_id": "mutable:latest"})
			if got, _ := payload["runtime_image_id"].(string); got != observed {
				t.Fatalf("%s image = %q, want %q", phase, got, observed)
			}
		}
	}
}

func (c runImageContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return c.status, c.err
}

func TestRunAgentPersistsActualImageEvidence(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		status *provider.ContainerStatus
		err    error
		want   string
	}{
		{"actual source", &provider.ContainerStatus{ID: "c1", ImageID: imageID}, nil, imageID},
		{"unsupported", &provider.ContainerStatus{ID: "c1"}, nil, ""},
		{"mutable tag", &provider.ContainerStatus{ID: "c1", ImageID: "mutable:latest"}, nil, ""},
		{"wrong container", &provider.ContainerStatus{ID: "other", ImageID: imageID}, nil, ""},
		{"failed inspect", nil, errors.New("unavailable"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newLockedMemState()
			o := New(runImageContainer{status: tc.status, err: tc.err}, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
			journal := &chunkRecorder{}
			o.SetJournal(journal)
			err := o.RunAgent(context.Background(), AgentRunRequest{RunID: "run-image", AgentID: "a1", AgentSlug: "agent-1", ChatID: "s1", ContainerID: "c1", CLIAdapter: "CLAUDE_CODE", UserMessage: "go", TimeoutSecs: 30}, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := state.Get(context.Background(), "agent_runs", "run-image")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got["status"] != "completed" {
				t.Fatalf("terminal state = %s", raw)
			}
			if value, _ := got["runtime_image_id"].(string); value != tc.want {
				t.Errorf("run evidence = %q, want %q", value, tc.want)
			}
			journal.mu.Lock()
			defer journal.mu.Unlock()
			count := 0
			for _, entry := range journal.entries {
				if entry.Type != "exec.command" {
					continue
				}
				count++
				payload := entry.Payload
				if value, _ := payload["runtime_image_id"].(string); value != tc.want {
					t.Errorf("event evidence = %q, want %q", value, tc.want)
				}
			}
			if count < 2 {
				t.Fatalf("only %d exec events", count)
			}
		})
	}
}
