//go:build !clionly

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func TestInitProvidersRejectsUnsupportedKubernetes(t *testing.T) {
	for _, skipDocker := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal_start", true: "no_docker"}[skipDocker], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Container.Provider = "k8s"
			deps, err := initProviders(context.Background(), cfg, nil, covLogger(), skipDocker)
			if deps != nil {
				t.Cleanup(deps.Close)
			}
			if err == nil {
				t.Fatal("configured k8s silently booted without a container provider")
			}
			for _, text := range []string{"k8s", "not implemented", "docker", "apple", "auto"} {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("startup error lacks actionable %q: %v", text, err)
				}
			}
		})
	}
}
