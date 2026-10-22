package config

import (
	"errors"
	"github.com/crewship-ai/crewship/internal/appengine"
)

type GridCraftConfig struct {
	Enabled             bool   `yaml:"enabled"`
	ServiceSocket       string `yaml:"service_socket"`
	ServiceTokenFile    string `yaml:"service_token_file"`
	MaxWorkers          int    `yaml:"max_workers"`
	MaxWorkspaceWorkers int    `yaml:"max_workspace_workers"`
	// EditorEnabled is the operator switch of live workbook editing (editor
	// sessions, cell edits and the commits feed); the workspace flag
	// gridcraft_editor must be on as well.
	EditorEnabled bool `yaml:"editor_enabled"`
}

func (c GridCraftConfig) validate() error {
	if !c.Enabled {
		if c.EditorEnabled {
			return errors.New("gridcraft editor requires the gridcraft workbook API to be enabled")
		}
		return nil
	}
	if _, err := appengine.NewWorkerClient(c.ServiceSocket, c.ServiceTokenFile); err != nil {
		return errors.New("gridcraft worker requires distinct clean absolute private service paths <=100 bytes")
	}
	if c.MaxWorkers < 0 || c.MaxWorkers > 16 || c.MaxWorkspaceWorkers < 0 || c.MaxWorkspaceWorkers > 16 {
		return errors.New("gridcraft worker limits must be between 0 (default) and 16")
	}
	return nil
}
