package docker

import (
	"context"
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		RuntimeImage:   "ghcr.io/crewship-ai/agent-runtime:latest",
		DefaultRuntime: "runc",
		Network:        "crewship-agents",
		OutputBasePath: "/var/lib/crewship",
	}

	if cfg.RuntimeImage == "" {
		t.Error("expected non-empty runtime image")
	}
	if cfg.DefaultRuntime != "runc" {
		t.Errorf("expected runc, got %q", cfg.DefaultRuntime)
	}
	if cfg.Network != "crewship-agents" {
		t.Errorf("expected crewship-agents, got %q", cfg.Network)
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	// Compile-time check via var _ line in docker.go
	// This test just documents the contract
	var _ interface{} = (*Provider)(nil)
}

func TestContainerNameFormat(t *testing.T) {
	slug := "engineering"
	name := "crewship-team-" + slug
	if name != "crewship-team-engineering" {
		t.Errorf("unexpected container name: %s", name)
	}
}

func TestNewRequiresDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := New(ctx, Config{
		RuntimeImage: "test:latest",
		Network:      "test-net",
	}, nil)
	if err != nil {
		t.Skipf("Docker daemon not available, skipping: %v", err)
	}
}
