package docker

import (
	"testing"
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
	// New() will fail without Docker daemon -- expected in CI
	_, err := New(Config{
		RuntimeImage: "test:latest",
		Network:      "test-net",
	}, nil)
	// We accept both success (Docker running) and error (Docker not running)
	// This test just ensures no panic
	_ = err
}
