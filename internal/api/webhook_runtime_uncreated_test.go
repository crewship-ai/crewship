package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestWebhookRuntime_ReturnBeforeCreationNeedsNoProviderProbe(t *testing.T) {
	// The provider cannot probe a runtime (as on the live direct-exec profile).
	// A RunAgent return while waiting for admission must retain the stronger
	// fact that the creation gate never admitted a process.
	rt := NewWebhookRuntime(&WebhookHandler{
		orch: orchestrator.New(covAsg2Provider{}, nil, newTestLogger()),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launch := &webhookLaunch{
		cancel: cancel, returned: true,
		location: &orchestrator.RunLocation{RunID: "uncreated-run", ContainerID: "test-container", AgentSlug: "test-agent"},
	}
	rt.launches.Store("uncreated-run", launch)
	if alive, err := rt.Alive(context.Background(), "agent-run:uncreated-run"); err != nil || alive {
		t.Fatalf("never requested a process: alive=%v err=%v", alive, err)
	}
	if stopped, err := rt.Stop(context.Background(), "agent-run:uncreated-run"); err != nil || !stopped {
		t.Fatalf("never requested a process: stopped=%v err=%v", stopped, err)
	}
	if ctx.Err() == nil {
		t.Fatal("stop must close the creation path")
	}
	// Once creation WAS requested, a returned call no longer proves absence.
	// Probe failure must remain uncertainty, never a false confirmed stop.
	launch.requested = true
	if _, err := rt.Alive(context.Background(), "agent-run:uncreated-run"); err == nil {
		t.Fatal("requested process must still use the provider probe")
	}
}
