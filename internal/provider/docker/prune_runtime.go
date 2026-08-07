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
	"github.com/moby/moby/client"
)

// PruneCrewRuntimes removes the LIVE id-scoped runtime resources of each crew:
//
//   - agent container  "<prefix>-team-<slug>-<id>"
//   - home volume      "<prefix>-home-<slug>-<id>"
//   - tools volume     "<prefix>-tools-<slug>-<id>"
//   - sidecar container(s)  (labels crewship.crew-id=<id>, crewship.kind=sidecar)
//   - sidecar volumes       (labels crewship.crew-id=<id>, crewship.kind=sidecar-volume)
//
// Sidecars used to be matched here by the crewship.crew SLUG label and by the
// volume name prefix "<prefix>-svc-<slug>-vol-". Both reach across tenants
// (#1732): slugs are UNIQUE(workspace_id, slug), so nuking one workspace's
// crews removed another workspace's identically-slugged live sidecars and
// deleted their data volumes. Matching the globally-unique crew id label
// cannot. Pre-#1732 slug-only sidecar resources are re-keyed by
// migrateLegacySidecarResources the first time the crew's services start;
// any that are still on the daemon are reported here, not removed.
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

	prefix := p.namePrefix()

	// Build the exact-match target sets (agent container + its named volumes)
	// and the sidecar match keys (label for containers, name-prefix for
	// volumes). A ref missing id or slug can't form an unambiguous id-scoped
	// name — skip it rather than risk matching a legacy (slug-only) resource.
	targetContainers := make(map[string]bool, len(crews))
	targetVolumes := make(map[string]bool, len(crews)*2)
	sidecarCrewIDs := make(map[string]bool, len(crews))
	legacySidecarVolPrefixes := make([]string, 0, len(crews))
	for _, c := range crews {
		if c.ID == "" || c.Slug == "" {
			continue
		}
		targetContainers[p.CrewContainerName(c.ID, c.Slug)] = true
		targetVolumes[p.homeVolumeName(c.ID, c.Slug)] = true
		targetVolumes[p.toolsVolumeName(c.ID, c.Slug)] = true
		sidecarCrewIDs[c.ID] = true
		legacySidecarVolPrefixes = append(legacySidecarVolPrefixes, prefix+"-svc-"+c.Slug+"-vol-")
	}
	if len(targetContainers) == 0 && len(sidecarCrewIDs) == 0 {
		return removed, nil
	}

	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return removed, fmt.Errorf("list containers (crew runtime prune): %w", err)
	}
	for _, c := range listResult.Items {
		name, match := "", false
		for _, n := range c.Names {
			trimmed := strings.TrimPrefix(n, "/")
			if targetContainers[trimmed] {
				name, match = trimmed, true
				break
			}
		}
		// Sidecar names embed the crew id but also the service name, which this
		// function does not have; match them by their crew-id label instead.
		if !match && c.Labels[crewKindLabel] == sidecarKind && sidecarCrewIDs[c.Labels[crewCrewIDLabel]] {
			match = true
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			} else {
				name = provider.ShortID(c.ID)
			}
		}
		if !match {
			continue
		}
		if _, rmErr := p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); rmErr != nil {
			p.logger.Warn("crew runtime container remove failed", "container", name, "error", rmErr)
		} else {
			removed = append(removed, name)
		}
	}

	volList, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return removed, fmt.Errorf("list volumes (crew runtime prune): %w", err)
	}
	for _, vol := range volList.Items {
		match := targetVolumes[vol.Name] ||
			(vol.Labels[crewKindLabel] == sidecarVolumeKind && sidecarCrewIDs[vol.Labels[crewCrewIDLabel]])
		if !match {
			// Pre-#1732 sidecar volumes carry no crew id, in the name or in a
			// label, so nothing here can prove which crew owns one — and this
			// crew's slug is shared with any identically-slugged crew in another
			// workspace. Name them for the operator; do NOT delete another
			// tenant's data directory on a guess.
			for _, pfx := range legacySidecarVolPrefixes {
				if strings.HasPrefix(vol.Name, pfx) && vol.Labels[crewCrewIDLabel] == "" {
					p.logger.Warn("legacy slug-scoped sidecar volume left in place: it carries no crew id, so ownership cannot be proven — start the crew once to migrate it (#1732), or remove it by hand",
						"volume", vol.Name)
					break
				}
			}
			continue
		}
		if _, rmErr := p.client.VolumeRemove(ctx, vol.Name, client.VolumeRemoveOptions{Force: true}); rmErr != nil {
			p.logger.Warn("crew runtime volume remove failed", "volume", vol.Name, "error", rmErr)
		} else {
			removed = append(removed, vol.Name)
		}
	}

	return removed, nil
}
