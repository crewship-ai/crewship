//go:build !clionly

package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func TestDockerProviderReportsReadinessOnlyWhenExplicit(t *testing.T) {
	for provider, want := range map[string]bool{"docker": true, "auto": false, "apple": false} {
		cfg := config.Default()
		cfg.Container.Provider = provider
		if got := dockerProviderConfig(cfg, nil).ReportRuntimeReadiness; got != want {
			t.Errorf("container.provider=%q: ReportRuntimeReadiness=%v, want %v", provider, got, want)
		}
	}
}
