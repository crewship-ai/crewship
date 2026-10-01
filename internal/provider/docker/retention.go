package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Idle retention (resourcelifecycle.Retention) runs on the provider itself,
// not a separate client: removing a runtime must hold the same per-crew lock
// EnsureCrewRuntime takes and drop the warm-crew fact, or a start racing the
// removal would trust a container id that no longer exists.
var _ resourcelifecycle.RetentionRuntime = (*Provider)(nil)

func (p *Provider) ListContainers(ctx context.Context) ([]resourcelifecycle.RetentionContainer, error) {
	result, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]resourcelifecycle.RetentionContainer, 0, len(result.Items))
	for _, c := range result.Items {
		provisioning := false
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), devcontainer.TempContainerNamePrefix) {
				provisioning = true
			}
		}
		out = append(out, resourcelifecycle.RetentionContainer{
			ID: c.ID, State: string(c.State), ImageID: c.ImageID,
			InstanceID: c.Labels[resourcelifecycle.InstanceLabel], CrewID: c.Labels[crewCrewIDLabel], Kind: c.Labels[crewKindLabel],
			Provisioning: provisioning,
		})
	}
	return out, nil
}

func (p *Provider) InspectContainer(ctx context.Context, id string) (resourcelifecycle.RetentionContainer, error) {
	result, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return resourcelifecycle.RetentionContainer{}, cleanupError(err)
	}
	c := result.Container
	if c.Config == nil || c.State == nil || c.ID == "" {
		return resourcelifecycle.RetentionContainer{}, fmt.Errorf("incomplete container inspection")
	}
	out := resourcelifecycle.RetentionContainer{
		ID: c.ID, State: string(c.State.Status), ImageID: c.Image,
		InstanceID: c.Config.Labels[resourcelifecycle.InstanceLabel], CrewID: c.Config.Labels[crewCrewIDLabel], Kind: c.Config.Labels[crewKindLabel],
	}
	// Docker reports "0001-01-01T00:00:00Z" for a container that never
	// stopped; that stays zero, which retention treats as unknown.
	if t, err := time.Parse(time.RFC3339Nano, c.State.FinishedAt); err == nil && t.Year() > 1 {
		out.FinishedAt = t
	}
	for _, m := range c.Mounts {
		out.Mounts = append(out.Mounts, resourcelifecycle.Mount{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	return out, nil
}

func (p *Provider) RemoveIdleRuntime(ctx context.Context, containerID, crewID string, verify func(resourcelifecycle.RetentionContainer) error) error {
	mu := p.lockForCrew(crewID)
	mu.Lock()
	defer mu.Unlock()
	fresh, err := p.InspectContainer(ctx, containerID)
	if err != nil {
		return err
	}
	if fresh.CrewID != crewID {
		return fmt.Errorf("container %s no longer belongs to crew %s", containerID, crewID)
	}
	if err := verify(fresh); err != nil {
		return err
	}
	// Force=false: Docker refuses a container that started meanwhile.
	// RemoveVolumes=false: anonymous and named volumes, and the host data
	// behind bind-backed ones, all stay.
	if _, err := p.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); err != nil {
		return cleanupError(err)
	}
	p.evictWarm(crewID)
	return nil
}

func (p *Provider) ListCacheImages(ctx context.Context) ([]resourcelifecycle.CacheImage, error) {
	result, err := p.client.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	var out []resourcelifecycle.CacheImage
	for _, img := range result.Items {
		var refs []string
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, localCacheImagePrefix) {
				refs = append(refs, tag)
			}
		}
		if len(refs) == 0 {
			continue
		}
		out = append(out, resourcelifecycle.CacheImage{ID: img.ID, Refs: refs, Created: time.Unix(img.Created, 0), Size: img.Size})
	}
	return out, nil
}

func (p *Provider) RemoveImage(ctx context.Context, ref string) error {
	// Force=false: Docker refuses an image any container still uses.
	_, err := p.client.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: false, PruneChildren: true})
	return err
}
