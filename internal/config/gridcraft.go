package config

import (
	"errors"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type GridCraftConfig struct {
	Enabled             bool   `yaml:"enabled"`
	Image               string `yaml:"image"`
	MaxWorkers          int    `yaml:"max_workers"`
	MaxWorkspaceWorkers int    `yaml:"max_workspace_workers"`
}

func (c GridCraftConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if _, err := mooring.NewDockerRunner(c.Image); err != nil {
		return errors.New("gridcraft.image must pin a qualified image by sha256 digest")
	}
	if c.MaxWorkers < 0 || c.MaxWorkers > 16 || c.MaxWorkspaceWorkers < 0 || c.MaxWorkspaceWorkers > 16 {
		return errors.New("gridcraft worker limits must be between 0 (default) and 16")
	}
	return nil
}
