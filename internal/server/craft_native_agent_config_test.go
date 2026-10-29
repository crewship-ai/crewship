package server

import (
	"github.com/crewship-ai/crewship/internal/config"
	"testing"
)

func TestNativeCraftAgentEngineRequiresExplicitStartupOptIn(t *testing.T) {
	cfg := &config.Config{}
	out, err := nativeCraftHTTPConfig(cfg, nil)
	if err != nil || out.AgentsEnabled || out.AgentApply != nil || out.AgentInspect != nil {
		t.Fatal("default enabled native agents")
	}
	cfg.CraftNative.AgentsEnabled = true
	out, err = nativeCraftHTTPConfig(cfg, nil)
	if err != nil || !out.AgentsEnabled || out.AgentApply == nil || out.AgentInspect == nil {
		t.Fatal("explicit native agent engine not wired")
	}
}
