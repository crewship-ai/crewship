package docker

import (
	"strings"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Preserve inherited image keys only as blank entries for the trusted keeper.
// Their values are restored by host-controlled exec after the boot is sealed.
func keeperEnv(env []string, img map[string]string) []string {
	keys := map[string]bool{}
	for k := range img {
		keys[k] = true
	}
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		keys[k] = true
	}
	out := []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}
	for k := range keys {
		if k != "PATH" && k != "HOME" {
			out = append(out, k+"=")
		}
	}
	return out
}

// registerStagedEnvScope records only authentic immutable scope metadata.
// Callers recovering inventory must pass an authenticated Docker inspect, not
// list labels or a caller-provided container alias. No filesystem sweep occurs.
func (p *Provider) registerStagedEnvScope(c container.InspectResponse) error {
	if err := p.validateStagedEnvScope(c); err != nil {
		return err
	}
	copy := container.InspectResponse{Config: &container.Config{Labels: map[string]string{
		resourcelifecycle.InstanceLabel: c.Config.Labels[resourcelifecycle.InstanceLabel],
		crewCrewIDLabel:                 c.Config.Labels[crewCrewIDLabel],
		stagedEnvLabel:                  c.Config.Labels[stagedEnvLabel],
	}}}
	copy.ID, copy.Image = c.ID, c.Image
	p.stagedEnvScopes.Store(copy.ID, copy)
	return nil
}
