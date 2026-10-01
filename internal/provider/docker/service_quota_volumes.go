package docker

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
)

const quotaBytesLabel = "crewship.svc.quota-bytes"
const quotaGenerationLabel = "crewship.svc.quota-generation"

func validateQuotaService(svc *provider.CrewService) error {
	for _, v := range svc.Volumes {
		if err := quota.ValidateVolume(svc.QuotaEnforced, svc.Name, v.Name, v.Mount, v.Generation, v.QuotaBytes); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) quotaServiceVolumes(ctx context.Context, crewID, crewSlug string, svc *provider.CrewService) ([]mount.Mount, error) {
	if err := validateQuotaService(svc); err != nil {
		return nil, err
	}
	if !svc.QuotaEnforced {
		return nil, nil
	}
	catalog, ok := p.cfg.QuotaCatalog.(quota.ReferenceCatalog)
	if !ok || catalog == nil {
		return nil, quota.ErrUnavailable
	}
	mounts := make([]mount.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		gen := v.Generation
		if gen == 0 {
			gen = 1
		}
		key := quota.Key{Crew: crewID, Service: svc.Name, Volume: v.Name, Generation: gen}
		d, err := catalog.Ensure(ctx, key, v.QuotaBytes, quota.Owner{})
		if err != nil {
			return nil, err
		}
		if d.Key != key || d.Bytes != v.QuotaBytes || d.ID == "" || !path.IsAbs(d.Mount) || path.Clean(d.Mount) != d.Mount {
			return nil, quota.ErrDenied
		}
		name := p.namePrefix() + "-quota-" + d.ID
		labels := sidecarVolumeLabels(crewID, crewSlug, svc.Name, v.Name)
		labels[quotaBytesLabel] = strconv.FormatInt(d.Bytes, 10)
		labels[quotaGenerationLabel] = strconv.FormatInt(gen, 10)
		if err = catalog.Protect(ctx, key, name); err != nil {
			return nil, err
		}
		options := map[string]string{"type": "none", "o": "bind", "device": d.Mount}
		existing, err := p.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if err == nil {
			volume := existing.Volume
			if volume.Driver != "local" || !reflect.DeepEqual(volume.Options, options) {
				return nil, quota.ErrDenied
			}
			for k, value := range volumeLabels(labels) {
				// The slug is a display label that a crew rename changes;
				// crew id, service, volume, capacity and generation are
				// the filesystem's identity.
				if k == crewCrewLabel {
					continue
				}
				if volume.Labels[k] != value {
					return nil, quota.ErrDenied
				}
			}
		} else {
			if !cerrdefs.IsNotFound(err) {
				return nil, err
			}
			if _, err = p.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", DriverOpts: options, Labels: volumeLabels(labels)}); err != nil {
				return nil, err
			}
		}
		if _, err = catalog.Verify(ctx, key, v.QuotaBytes); err != nil {
			return nil, err
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: v.Mount, VolumeOptions: &mount.VolumeOptions{NoCopy: true}})
	}
	return mounts, nil
}

func (p *Provider) validateQuotaImage(ctx context.Context, svc *provider.CrewService) error {
	if !svc.QuotaEnforced {
		return nil
	}
	image, err := p.client.ImageInspect(ctx, svc.Image)
	if err != nil {
		return err
	}
	if image.Config == nil {
		return quota.ErrDenied
	}
	for target := range image.Config.Volumes {
		found := false
		for _, v := range svc.Volumes {
			if target == v.Mount {
				found = true
				break
			}
		}
		if !found {
			return quota.ErrDenied
		}
	}
	return nil
}

func checkQuotaMounts(actual, desired []mount.Mount) error {
	if len(actual) != len(desired) {
		return fmt.Errorf("quota service mount count drift")
	}
	for _, want := range desired {
		found := false
		for _, got := range actual {
			if got.Target != want.Target {
				continue
			}
			if got.Type != mount.TypeVolume || got.Source != want.Source || got.ReadOnly || got.VolumeOptions == nil || !got.VolumeOptions.NoCopy || got.BindOptions != nil {
				return fmt.Errorf("quota service mount drift at %s", want.Target)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("quota service missing mount at %s", want.Target)
		}
	}
	return nil
}
