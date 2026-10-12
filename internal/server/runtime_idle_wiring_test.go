package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/provider"
)

type runtimeIdleHookContainer struct {
	provider.ContainerProvider
	verify func(context.Context, string, string) error
}

func (c *runtimeIdleHookContainer) SetRuntimeIdleVerifier(fn func(context.Context, string, string) error) {
	c.verify = fn
}
func TestBuildOrchestratorManagedIdleHook(t *testing.T) {
	c := &runtimeIdleHookContainer{}
	buildOrchestrator(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), c, nil)
	if c.verify == nil {
		t.Fatal("production construction left runtime activation unconfigured")
	}
	if err := c.verify(t.Context(), "crew", "runtime"); err == nil {
		t.Fatal("missing durable state was accepted as idle")
	}
}
