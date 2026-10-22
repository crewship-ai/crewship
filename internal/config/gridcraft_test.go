package config

import (
	"testing"
)

func TestGridCraftDefaultOffAndPrivateServicePaths(t *testing.T) {
	c := Default()
	if c.GridCraft.Enabled {
		t.Fatal("default on")
	}
	if err := (GridCraftConfig{Enabled: true, ServiceSocket: "worker.sock"}).validate(); err == nil {
		t.Fatal("relative socket accepted")
	}
	if err := (GridCraftConfig{Enabled: true, ServiceSocket: "/run/workbook/service.sock", ServiceTokenFile: "/run/secrets/worker-token"}).validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_ENABLED", "true")
	t.Setenv("CREWSHIP_GRIDCRAFT_SERVICE_SOCKET", "/run/workbook/service.sock")
	t.Setenv("CREWSHIP_GRIDCRAFT_SERVICE_TOKEN_FILE", "/run/secrets/worker-token")
	applyEnvOverrides(c)
	if !c.GridCraft.Enabled || c.GridCraft.ServiceSocket != "/run/workbook/service.sock" || c.GridCraft.ServiceTokenFile != "/run/secrets/worker-token" {
		t.Fatal("operator override lost")
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_ENABLED", "misspelled")
	applyEnvOverrides(c)
	if c.GridCraft.Enabled {
		t.Fatal("invalid flag opened runtime")
	}
}
