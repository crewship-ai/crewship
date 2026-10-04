//go:build !linux

package docker

import "github.com/moby/moby/api/types/container"

const stagedEnvLabel = "crewship.staged-env-ref"

func newStagedEnvHandle() string                                            { return "" }
func (p *Provider) saveStagedEnv(container.InspectResponse, []string) error { return errStagedDenied }
func (p *Provider) loadStagedEnv(container.InspectResponse) ([]string, error) {
	return nil, errStagedDenied
}

func (p *Provider) removeStagedEnv(container.InspectResponse) error { return errStagedDenied }

func (p *Provider) validateStagedEnvScope(container.InspectResponse) error { return errStagedDenied }
