package docker

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestComputeSidecarSpecHash_StableAcrossEnvOrdering(t *testing.T) {
	a := provider.CrewService{
		Name:  "pg",
		Image: "postgres:16",
		Env: map[string]string{
			"POSTGRES_DB":   "app",
			"POSTGRES_USER": "postgres",
		},
		Ports: []string{"5432"},
	}
	b := provider.CrewService{
		Name:  "pg",
		Image: "postgres:16",
		// Same content, different insertion order.
		Env: map[string]string{
			"POSTGRES_USER": "postgres",
			"POSTGRES_DB":   "app",
		},
		Ports: []string{"5432"},
	}
	if computeSidecarSpecHash(&a) != computeSidecarSpecHash(&b) {
		t.Error("hash should be stable across env map iteration order")
	}
}

func TestComputeSidecarSpecHash_DetectsCommandDrift(t *testing.T) {
	base := provider.CrewService{Name: "x", Image: "alpine:3"}
	withCmd := base
	withCmd.Command = []string{"sleep", "infinity"}
	if computeSidecarSpecHash(&base) == computeSidecarSpecHash(&withCmd) {
		t.Error("hash should change when command changes")
	}
}

func TestComputeSidecarSpecHash_DetectsHealthcheckDrift(t *testing.T) {
	base := provider.CrewService{Name: "x", Image: "redis:7"}
	withHc := base
	withHc.Healthcheck = &provider.CrewServiceHealthcheck{
		Test:     []string{"CMD", "redis-cli", "ping"},
		Interval: 5 * time.Second,
	}
	if computeSidecarSpecHash(&base) == computeSidecarSpecHash(&withHc) {
		t.Error("hash should change when healthcheck added")
	}
}

func TestComputeSidecarSpecHash_DetectsVolumeMountDrift(t *testing.T) {
	base := provider.CrewService{
		Name:  "pg",
		Image: "postgres:16",
		Volumes: []provider.CrewServiceVolume{
			{Name: "data", Mount: "/var/lib/postgresql/data"},
		},
	}
	withDifferentMount := base
	withDifferentMount.Volumes = []provider.CrewServiceVolume{
		{Name: "data", Mount: "/var/lib/postgresql/other"},
	}
	if computeSidecarSpecHash(&base) == computeSidecarSpecHash(&withDifferentMount) {
		t.Error("hash should change when mount path changes")
	}
}

func TestComputeSidecarSpecHash_ImageNotInHash(t *testing.T) {
	// Image is checked separately so its diff produces a specific
	// error message. Including it in the hash would double-report.
	a := provider.CrewService{Name: "x", Image: "postgres:15"}
	b := provider.CrewService{Name: "x", Image: "postgres:16"}
	if computeSidecarSpecHash(&a) != computeSidecarSpecHash(&b) {
		t.Error("image must not contribute to spec hash (separate drift path)")
	}
}
