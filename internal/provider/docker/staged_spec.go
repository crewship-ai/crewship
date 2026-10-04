package docker

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/moby/moby/api/types/container"
)

// Pin the image before reading its configuration and inherited environment.
func (p *Provider) buildStagedCrewContainerConfig(ctx context.Context, team provider.CrewConfig, name, image, runtime string, memoryMB int, cpus float64, dirs crewDirs) (*container.Config, *container.HostConfig, []string, error) {
	img, e := p.client.ImageInspect(ctx, image)
	if e != nil {
		return nil, nil, nil, e
	}
	if img.ID == "" || img.Config == nil || len(img.Config.Volumes) > 0 {
		return nil, nil, nil, fmt.Errorf("%w: image VOLUME or unavailable identity unsupported", errStagedDenied)
	}
	c, h, e := p.buildCrewContainerConfig(ctx, team, name, img.ID, runtime, memoryMB, cpus, dirs)
	if e != nil {
		return nil, nil, nil, e
	}
	inherited, e := imageEnvMap(ctx, p.client, img.ID)
	if e != nil {
		return nil, nil, nil, e
	}
	env, e := p.stageCrewSpec(team, c, h, inherited)
	if e != nil {
		return nil, nil, nil, e
	}
	source, digest, e := p.stageArtifact(ctx, img.ID)
	if e != nil {
		return nil, nil, nil, e
	}
	bootstrap, e := p.stageTrustedFile(p.cfg.EntrypointPath, 1<<20)
	if e != nil {
		return nil, nil, nil, e
	}
	c.Image = img.ID
	c.Labels["crewship.keeper-sha256"] = digest
	for i := range h.Mounts {
		switch h.Mounts[i].Target {
		case stagedstart.Binary:
			h.Mounts[i].Source = source
		case stagedstart.Bootstrap:
			h.Mounts[i].Source = bootstrap
		}
	}
	return c, h, env, nil
}

func (p *Provider) stageCrewSpec(team provider.CrewConfig, c *container.Config, h *container.HostConfig, inherited map[string]string) ([]string, error) {
	if e := p.egressFenceApplicable(team); e != nil {
		return nil, e
	}
	if len(team.CapAdd) > 0 || len(team.SecurityOpt) > 0 {
		return nil, errStagedDenied
	}
	for _, m := range team.ExtraMounts {
		if overlap(m.Target, stagedstart.Binary) || overlap(m.Target, stagedstart.Bootstrap) || m.Source == "/var/run/docker.sock" {
			return nil, errStagedDenied
		}
	}
	env := append([]string(nil), c.Env...)
	c.User = "1002:1002"
	c.Entrypoint = []string{stagedstart.Binary}
	c.Cmd = []string{"--staged-keeper"}
	c.Labels[stagedstart.Label] = stagedstart.Version
	c.Labels[stagedEnvLabel] = newStagedEnvHandle()
	c.Env = keeperEnv(env, inherited)
	off := false
	h.Init = &off
	return env, nil
}
