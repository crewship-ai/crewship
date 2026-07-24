package pipeline

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// blockingAgentContainer streams the agent's usage envelope, then blocks the
// reader open (never EOF) until the stream is closed by a context cancel —
// modelling a real "cancel mid-agent-step" where the provider already
// reported the tokens spent so far.
type blockingAgentContainer struct {
	orchCovContainer
	preamble string // bytes delivered before the reader blocks
}

func (m *blockingAgentContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	m.execScripts = append(m.execScripts, joined)
	if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-cov-agent") {
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, m.preamble)
			// Leave pw open so the reader blocks after the preamble until
			// streamOutput closes pr on ctx cancel.
		}()
		return &provider.ExecResult{ExecID: "exec-agent", Reader: pr}, nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}

// newOrchRunnerRigProvider is newOrchRunnerRig but parameterised on any
// ContainerProvider so a test can supply a custom streaming container.
func newOrchRunnerRigProvider(t *testing.T, container provider.ContainerProvider, resolver *orchCovResolver) *OrchestratorRunner {
	t.Helper()
	db := openAgentResolverTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew_cov', 'ws_cov', 'cov-crew', 'Cov');
INSERT INTO agents (id, crew_id, slug) VALUES ('agent_cov', 'crew_cov', 'cov-agent');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(container, newOrchCovState(), logger)
	r, err := NewOrchestratorRunner(OrchestratorRunnerDeps{
		DB:        db,
		Orch:      orch,
		Container: container,
		Resolver:  resolver,
		LogWriter: logcollector.NewWriter(t.TempDir(), logger),
		ConvStore: conversation.NewStore(t.TempDir(), logger),
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("construct runner: %v", err)
	}
	return r
}

// #1426 (3.4) — cancelling a run mid-agent-step must still record the partial
// cost + tokens the provider already reported, not $0.
func TestOrchestratorRunner_RunStep_PartialCostOnCancel(t *testing.T) {
	container := &blockingAgentContainer{
		preamble: "partial work\n" +
			`{"type":"result","subtype":"success","total_cost_usd":0.42,"usage":{"input_tokens":7,"output_tokens":13}}` + "\n",
	}
	r := newOrchRunnerRigProvider(t, container, &orchCovResolver{info: covChatInfo()})

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		res AgentStepResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.RunStep(ctx, AgentStepRequest{
			WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
			Prompt: "do work", TimeoutSec: 30, PipelineID: "pln_cov", StepID: "s1",
		})
		done <- outcome{res, err}
	}()

	// Give the stream time to deliver the usage envelope, then cancel.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case o := <-done:
		if o.err == nil {
			t.Fatalf("expected a cancellation error, got nil")
		}
		if o.res.CostUSD != 0.42 {
			t.Errorf("partial cost not reported on cancel: got %v, want 0.42", o.res.CostUSD)
		}
		if o.res.TokensIn != 7 || o.res.TokensOut != 13 {
			t.Errorf("partial tokens not reported: got %d/%d, want 7/13", o.res.TokensIn, o.res.TokensOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunStep did not return after cancel")
	}
}
