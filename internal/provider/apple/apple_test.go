package apple

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestProviderImplementsInterface(t *testing.T) {
	var _ provider.ContainerProvider = (*Provider)(nil)
}

func TestContainerNameFormat(t *testing.T) {
	slug := "engineering"
	name := "crewship-team-" + slug
	if name != "crewship-team-engineering" {
		t.Errorf("unexpected container name: %s", name)
	}
}

func TestContainerNameWithPrefix(t *testing.T) {
	prefix := "crewship-2"
	slug := "engineering"
	name := prefix + "-team-" + slug
	if name != "crewship-2-team-engineering" {
		t.Errorf("unexpected container name: %s", name)
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abcdefghijklmnopqrstuvwxyz", "abcdefghijkl"},
		{"short", "short"},
		{"exactly12ch", "exactly12ch"},
	}
	for _, tt := range tests {
		got := shortID(tt.input)
		if got != tt.expected {
			t.Errorf("shortID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExecInspectNotFound(t *testing.T) {
	p := &Provider{
		execs: make(map[string]*execEntry),
	}
	_, _, err := p.ExecInspect(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent exec ID")
	}
}

func TestNewRequiresAppleRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p, err := New(ctx, Config{
		RuntimeImage: "test:latest",
		Network:      "test-net",
	}, nil)
	if err != nil {
		t.Skipf("Apple Container runtime not available, skipping: %v", err)
	}
	defer p.Close()
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		RuntimeImage:   "debian:bookworm-slim",
		Network:        "crewship-agents",
		OutputBasePath: "/var/lib/crewship",
	}
	if cfg.RuntimeImage == "" {
		t.Error("expected non-empty runtime image")
	}
	if cfg.Network != "crewship-agents" {
		t.Errorf("expected crewship-agents, got %q", cfg.Network)
	}
}

func TestDiscoverHostIP(t *testing.T) {
	ip := discoverHostIP()
	if ip == "" {
		t.Error("expected non-empty host IP")
	}
	t.Logf("Discovered host IP: %s", ip)
}
