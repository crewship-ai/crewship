package docker

import (
	"context"
	"io"
	"reflect"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// QuotaSnapshotNamespace is host configuration; it is never accepted from a
// bundle as a destination selector.
func (p *Provider) QuotaSnapshotNamespace() string {
	if catalog, ok := p.cfg.QuotaCatalog.(interface{ SnapshotNamespace() string }); ok {
		return catalog.SnapshotNamespace()
	}
	return ""
}

// ExportQuotaVolume is called only under the durable service maintenance fence.
// Remove stopped owned containers and Docker bind aliases before the helper
// examines mount namespaces. Any foreign alias makes volume removal fail.
func (p *Provider) ExportQuotaVolume(ctx context.Context, key quota.Key, size int64, dst io.Writer) error {
	if p.serviceGate() == nil {
		return quota.ErrUnavailable
	}
	catalog, ok := p.cfg.QuotaCatalog.(quota.SnapshotCatalog)
	if !ok {
		return quota.ErrUnavailable
	}
	mu := p.lockForCrew(key.Crew)
	mu.Lock()
	defer mu.Unlock()
	result, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range result.Items {
		if p.cfg.InstanceID == "" || c.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || !sidecarMatchesCrew(c.Labels, key.Crew, sidecarKind) || c.Labels[sidecarSvcLabel] != key.Service {
			continue
		}
		if c.State == "running" || c.State == "restarting" {
			return quota.ErrDenied
		}
		if _, err = p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{}); err != nil {
			return err
		}
	}
	d, err := catalog.Verify(ctx, key, size)
	if err != nil {
		return err
	}
	name := p.namePrefix() + "-quota-" + d.ID
	inspected, err := p.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err == nil {
		v := inspected.Volume
		if v.Driver != "local" || !reflect.DeepEqual(v.Options, map[string]string{"type": "none", "o": "bind", "device": d.Mount}) || !sidecarMatchesCrew(v.Labels, key.Crew, sidecarVolumeKind) || v.Labels[sidecarSvcLabel] != key.Service || v.Labels[sidecarVolNameLabel] != key.Volume || v.Labels[quotaBytesLabel] != strconv.FormatInt(size, 10) || v.Labels[quotaGenerationLabel] != strconv.FormatInt(key.Generation, 10) {
			return quota.ErrDenied
		}
		if _, err = p.client.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
			return err
		}
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}
	if err = catalog.Release(ctx, key, name); err != nil {
		return err
	}
	return catalog.Export(ctx, key, size, dst)
}

func (p *Provider) ImportQuotaVolume(ctx context.Context, key quota.Key, size int64, src io.Reader) error {
	if p.serviceGate() == nil {
		return quota.ErrUnavailable
	}
	catalog, ok := p.cfg.QuotaCatalog.(quota.SnapshotCatalog)
	if !ok {
		return quota.ErrUnavailable
	}
	mu := p.lockForCrew(key.Crew)
	mu.Lock()
	defer mu.Unlock()
	_, err := catalog.Import(ctx, key, size, src)
	return err
}

// DetachQuotaService preserves image generations while removing only exact
// stopped owned containers and their verified helper-backed Docker aliases.
func (p *Provider) DetachQuotaService(ctx context.Context, crew, service string) error {
	if p.serviceGate() == nil {
		return quota.ErrUnavailable
	}
	catalog, ok := p.cfg.QuotaCatalog.(quota.ReferenceCatalog)
	if !ok {
		return quota.ErrUnavailable
	}
	mu := p.lockForCrew(crew)
	mu.Lock()
	defer mu.Unlock()
	containers, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers.Items {
		if p.cfg.InstanceID == "" || c.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || !sidecarMatchesCrew(c.Labels, crew, sidecarKind) || c.Labels[sidecarSvcLabel] != service {
			continue
		}
		if c.State == "running" || c.State == "restarting" {
			return quota.ErrDenied
		}
		if _, err = p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{}); err != nil {
			return err
		}
	}
	volumes, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return err
	}
	for _, v := range volumes.Items {
		if !sidecarMatchesCrew(v.Labels, crew, sidecarVolumeKind) || v.Labels[sidecarSvcLabel] != service {
			continue
		}
		if v.Labels[quotaBytesLabel] == "" && v.Labels[quotaGenerationLabel] == "" {
			continue
		}
		size, sizeErr := strconv.ParseInt(v.Labels[quotaBytesLabel], 10, 64)
		gen, genErr := strconv.ParseInt(v.Labels[quotaGenerationLabel], 10, 64)
		key := quota.Key{Crew: crew, Service: service, Volume: v.Labels[sidecarVolNameLabel], Generation: gen}
		if sizeErr != nil || genErr != nil || quota.Validate(key, size) != nil {
			return quota.ErrDenied
		}
		d, err := catalog.Verify(ctx, key, size)
		if err != nil {
			return err
		}
		if v.Name != p.namePrefix()+"-quota-"+d.ID || v.Driver != "local" || !reflect.DeepEqual(v.Options, map[string]string{"type": "none", "o": "bind", "device": d.Mount}) {
			return quota.ErrDenied
		}
		if _, err = p.client.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{}); err != nil {
			return err
		}
		if err = catalog.Release(ctx, key, v.Name); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) RemoveQuotaVolume(ctx context.Context, key quota.Key) error {
	if p.serviceGate() == nil {
		return quota.ErrUnavailable
	}
	catalog, ok := p.cfg.QuotaCatalog.(quota.ReferenceCatalog)
	if !ok {
		return quota.ErrUnavailable
	}
	return catalog.Remove(ctx, key)
}
