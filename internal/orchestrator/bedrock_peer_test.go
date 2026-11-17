package orchestrator

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestBedrockPeerReusesSidecarWithOwnRouteIdentity(t *testing.T) {
	c := &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if !strings.Contains(strings.Join(cfg.Cmd, " "), "/health") {
			t.Fatalf("peer restarted or modified parent sidecar: %v", cfg.Cmd)
		}
		return covResult("health", `{"status":"ok","network_mode":"free","config_fingerprint":"parent-fingerprint"}`), nil
	}}
	o := New(c, newMemState(), covQuietLogger())
	o.SetSidecarEnabled(true)
	o.SetIPCConfig("http://crewshipd:8080", "master")
	req := bedrockRunFixture(t)
	req.SkipSidecar = true
	req.AgentID = "peer"
	req.RunID = "peer-run"
	req.WorkspaceID = "ws"
	req.CrewID = "crew"
	req.ContainerID = "container"
	env, err := o.ensureSidecar(t.Context(), &req, nil, req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	routeKey := internaltoken.DeriveLLMRouteKey("master", "ws", "crew")
	route := internaltoken.DeriveLLMRunRouteToken(routeKey, "peer", "peer-run") + internaltoken.RouteFingerprintDelimiter + "parent-fingerprint"
	if !req.sidecarActive || !strings.Contains(joined, route) || !strings.Contains(joined, "127.0.0.1:9119") || strings.Contains(joined, "never-in-agent-env") {
		t.Fatal("peer missing safe sidecar environment")
	}
	if len(c.snapshotCalls()) != 1 {
		t.Fatal("peer changed sidecar lifecycle")
	}
	req.NetworkMode = "restricted"
	if _, err := o.ensureSidecar(t.Context(), &req, nil, req.RunID); err == nil {
		t.Fatal("mismatched policy accepted")
	}
}
