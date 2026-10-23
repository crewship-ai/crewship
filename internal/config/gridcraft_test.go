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

// The live editor surface has its own operator switch, default off, which
// only takes effect together with the workbook API.
func TestGridCraftEditorOperatorSwitch(t *testing.T) {
	c := Default()
	if c.GridCraft.EditorEnabled {
		t.Fatal("editor default on")
	}
	if err := (GridCraftConfig{EditorEnabled: true}).validate(); err == nil {
		t.Fatal("editor accepted without the workbook API")
	}
	if err := (GridCraftConfig{Enabled: true, EditorEnabled: true, ServiceSocket: "/run/workbook/service.sock", ServiceTokenFile: "/run/secrets/worker-token"}).validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_ENABLED", "1")
	applyEnvOverrides(c)
	if !c.GridCraft.EditorEnabled {
		t.Fatal("operator override lost")
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_ENABLED", "yes")
	applyEnvOverrides(c)
	if c.GridCraft.EditorEnabled {
		t.Fatal("invalid flag opened the editor")
	}
}
