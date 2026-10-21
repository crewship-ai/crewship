package config

import (
	"strings"
	"testing"
)

func TestGridCraftDefaultOffAndDigestQualification(t *testing.T) {
	c := Default()
	if c.GridCraft.Enabled {
		t.Fatal("default on")
	}
	if err := (GridCraftConfig{Enabled: true, Image: "worker:latest"}).validate(); err == nil {
		t.Fatal("mutable worker accepted")
	}
	if err := (GridCraftConfig{Enabled: true, Image: "registry.example.test/workbook@sha256:" + strings.Repeat("a", 64)}).validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_ENABLED", "true")
	t.Setenv("CREWSHIP_GRIDCRAFT_IMAGE", "registry.example.test/workbook@sha256:"+strings.Repeat("b", 64))
	applyEnvOverrides(c)
	if !c.GridCraft.Enabled || !strings.HasSuffix(c.GridCraft.Image, strings.Repeat("b", 64)) {
		t.Fatal("operator override lost")
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_ENABLED", "misspelled")
	applyEnvOverrides(c)
	if c.GridCraft.Enabled {
		t.Fatal("invalid flag opened runtime")
	}
}
