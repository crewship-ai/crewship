package api

import (
	"context"
	"errors"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"testing"
)

type independentPrivateProbePanicProvider struct {
	independentPrivateDirectProbeProvider
}

func (*independentPrivateProbePanicProvider) ExecInspect(context.Context, string) (bool, int, error) {
	panic("independent-private-provider-diagnostic")
}
func TestIndependentPrivateLeafProbePanicIsOpaque(t *testing.T) {
	for _, method := range []string{"alive", "stop"} {
		t.Run(method, func(t *testing.T) {
			p := &independentPrivateProbePanicProvider{}
			owner := orchestrator.New(p, nil, newTestLogger())
			rt := &collaborationAgentTaskRuntime{WebhookRuntime: NewWebhookRuntime(&WebhookHandler{orch: owner, container: p})}
			loc := orchestrator.RunLocation{ContainerID: "private-container", AgentSlug: "private-agent", RunID: "private-run"}
			rt.launches.Store(loc.RunID, &webhookLaunch{requested: true, confirmed: true, nativeExecID: "original-exec", location: &loc})
			var caught any
			var err error
			func() {
				defer func() { caught = recover() }()
				if method == "alive" {
					_, err = rt.Alive(t.Context(), "agent-run:"+loc.RunID)
				} else {
					_, err = rt.Stop(t.Context(), "agent-run:"+loc.RunID)
				}
			}()
			if caught != nil || !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) {
				t.Errorf("private lifecycle provider panic escaped opaque boundary: panic=%v error=%v", caught, err)
			}
		})
	}
}
