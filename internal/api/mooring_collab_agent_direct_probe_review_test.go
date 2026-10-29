package api

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Independent synthetic-provider regression. This is a host boundary probe,
// not actual Docker execution, private-environment qualification or E3 evidence.
// The confirmed original exec remains alive while its PID file is not yet
// observable and there is no tmux session for this direct execution mode.
type independentPrivateDirectProbeProvider struct {
	provider.ContainerProvider
	inspectedOriginal int
}

func (p *independentPrivateDirectProbeProvider) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT\n"))}, nil
}
func (p *independentPrivateDirectProbeProvider) ExecInspect(_ context.Context, id string) (bool, int, error) {
	if id == "private-native-exec" {
		p.inspectedOriginal++
	}
	return true, 0, nil
}
func TestIndependentPrivateLeafNeverConfirmsMissingPIDAsNativeExecDeath(t *testing.T) {
	for _, method := range []string{"alive", "stop"} {
		t.Run(method, func(t *testing.T) {
			p := &independentPrivateDirectProbeProvider{}
			owner := orchestrator.New(p, nil, newTestLogger())
			rt := &collaborationAgentTaskRuntime{WebhookRuntime: NewWebhookRuntime(&WebhookHandler{orch: owner})}
			location := orchestrator.RunLocation{ContainerID: "private-container", AgentSlug: "private-agent", RunID: "private-run"}
			// Exact host launch phase after the real OnExecStarted callback. The leaf
			// currently retains actual exec identity only in onConfirmed callback DATA.
			launch := &webhookLaunch{requested: true, confirmed: true, location: &location}
			rt.launches.Store(location.RunID, launch)
			running, _, err := p.ExecInspect(t.Context(), "private-native-exec")
			if err != nil || !running {
				t.Fatal("original exec fixture is not alive")
			}
			p.inspectedOriginal = 0
			if method == "alive" {
				alive, err := rt.Alive(t.Context(), "agent-run:"+location.RunID)
				if !alive && err == nil {
					t.Errorf("private leaf declared original live exec absent from PID/tmux ABSENT; original ExecInspect calls=%d", p.inspectedOriginal)
				}
			} else {
				stopped, err := rt.Stop(t.Context(), "agent-run:"+location.RunID)
				if stopped && err == nil {
					t.Errorf("private leaf confirmed STOPPED for original live exec from PID/tmux ABSENT; original ExecInspect calls=%d", p.inspectedOriginal)
				}
			}
		})
	}
}
