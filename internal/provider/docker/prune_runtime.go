package docker

// Live (id-scoped) crew runtime teardown for the workspace full-teardown path
// (seed --nuke). Distinct from the legacy (slug-only) pruner in
// docker_container.go: that removes pre-C1 orphans instance-wide; this removes
// the CURRENT runtime of a specific set of crews and is called after their DB
// rows are (soft-)deleted, so nothing recreates them.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
)

// PruneCrewRuntimes removes the LIVE id-scoped runtime resources of each crew:
//
//   - agent container  "<prefix>-team-<slug>-<id>"
//   - home volume      "<prefix>-home-<slug>-<id>"
//   - tools volume     "<prefix>-tools-<slug>-<id>"
//   - sidecar container(s)  (labels crewship.crew=<slug>, crewship.kind=sidecar)
//   - sidecar volumes  "<prefix>-svc-<slug>-vol-*"
//
// Cached devcontainer images (crewship-cache:<hash>) are deliberately NOT
// touched — a reseed reuses them so no rebuild is forced. Satisfies
// provider.CrewRuntimePruner.
//
// The daemon is enumerated ONCE (not per crew). Containers are removed before
// volumes so docker won't refuse a still-attached volume. Per-resource removal
// failures are logged and skipped (a volume still "in use" must not wedge the
// rest); a transport failure listing the daemon is returned WITH whatever was
// removed so far so the caller can surface the partial result.
func (p *Provider) PruneCrewRuntimes(ctx context.Context, crews []provider.CrewRef) ([]string, error) {
	removed := []string{}
	if len(crews) == 0 {
		return removed, nil
	}

	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}

	// Build the exact-match target sets (agent container + its named volumes)
	// and the sidecar match keys (label for containers, name-prefix for
	// volumes). A ref missing id or slug can't form an unambiguous id-scoped
	// name — skip it rather than risk matching a legacy (slug-only) resource.
	targetContainers := make(map[string]bool, len(crews))
	targetVolumes := make(map[string]bool, len(crews)*2)
	sidecarSlugs := make(map[string]bool, len(crews))
	sidecarVolPrefixes := make([]string, 0, len(crews))
	for _, c := range crews {
		if c.ID == "" || c.Slug == "" {
			continue
		}
		targetContainers[p.CrewContainerName(c.ID, c.Slug)] = true
		targetVolumes[p.homeVolumeName(c.ID, c.Slug)] = true
		targetVolumes[p.toolsVolumeName(c.ID, c.Slug)] = true
		sidecarSlugs[c.Slug] = true
		sidecarVolPrefixes = append(sidecarVolPrefixes, prefix+"-svc-"+c.Slug+"-vol-")
	}
	if len(targetContainers) == 0 && len(sidecarSlugs) == 0 {
		return removed, nil
	}

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return removed, fmt.Errorf("list containers (crew runtime prune): %w", err)
	}
	for _, c := range containers {
		name, match := "", false
		for _, n := range c.Names {
			trimmed := strings.TrimPrefix(n, "/")
			if targetContainers[trimmed] {
				name, match = trimmed, true
				break
			}
		}
		// Sidecars carry no id-scoped name; match them by label instead.
		if !match && c.Labels["crewship.kind"] == "sidecar" && sidecarSlugs[c.Labels["crewship.crew"]] {
			match = true
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			} else {
				name = shortID(c.ID)
			}
		}
		if !match {
			continue
		}
		if rmErr := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			p.logger.Warn("crew runtime container remove failed", "container", name, "error", rmErr)
		} else {
			removed = append(removed, name)
		}
	}

	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return removed, fmt.Errorf("list volumes (crew runtime prune): %w", err)
	}
	for _, vol := range list.Volumes {
		if vol == nil {
			continue
		}
		match := targetVolumes[vol.Name]
		if !match {
			for _, pfx := range sidecarVolPrefixes {
				if strings.HasPrefix(vol.Name, pfx) {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}
		if rmErr := p.client.VolumeRemove(ctx, vol.Name, true); rmErr != nil {
			p.logger.Warn("crew runtime volume remove failed", "volume", vol.Name, "error", rmErr)
		} else {
			removed = append(removed, vol.Name)
		}
	}

	return removed, nil
}
