package docker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

const noexecBindVolumeKind = "crew-noexec-bind"

var _ resourcelifecycle.BindVolumeRuntime = (*cleanupRuntime)(nil)

// eligibleBindVolume excludes managed storage, named history volumes and
// unattributed legacy records. Removing a local bind volume only drops its
// metadata: its device directory remains on the host.
func eligibleBindVolume(v volume.Volume, instance string) bool {
	if instance == "" || v.Labels[resourcelifecycle.InstanceLabel] != instance ||
		v.Labels[crewKindLabel] != noexecBindVolumeKind || v.Labels[crewCrewIDLabel] == "" ||
		v.Driver != "local" || v.Options["type"] != "none" ||
		v.Options["o"] != noexecBindMountOpts || !filepath.IsAbs(v.Options["device"]) || len(v.Name) != 64 {
		return false
	}
	_, err := hex.DecodeString(v.Name)
	return err == nil
}

// ReapBindVolumes never prunes globally or forces a removal. Docker's
// dangling filter accounts for references from stopped containers; the final
// non-force remove also refuses a reference acquired after the inventory.
// A fresh inspect checks ownership again rather than trusting list filters.
func (r *cleanupRuntime) ReapBindVolumes(ctx context.Context, instance string, limit int) error {
	if instance == "" || limit <= 0 {
		return nil
	}
	listed, err := r.client.VolumeList(ctx, client.VolumeListOptions{Filters: make(client.Filters).
		Add("dangling", "true").
		Add("label", resourcelifecycle.InstanceLabel+"="+instance).
		Add("label", crewKindLabel+"="+noexecBindVolumeKind)})
	if err != nil {
		return fmt.Errorf("list unused bind volumes: %w", err)
	}
	if len(listed.Warnings) > 0 {
		return errors.New("incomplete bind volume inventory")
	}
	var failures []error
	attempts := 0
	for _, v := range listed.Items {
		if !eligibleBindVolume(v, instance) {
			continue
		}
		if attempts >= limit {
			break
		}
		if err := quiesce.Yield(ctx); err != nil {
			return errors.Join(resourcelifecycle.ErrBindVolumeYielded, err)
		}
		attempts++
		fresh, err := r.client.VolumeInspect(ctx, v.Name, client.VolumeInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("inspect bind volume: %w", err))
			continue
		}
		if fresh.Volume.Name != v.Name || !eligibleBindVolume(fresh.Volume, instance) {
			continue
		}
		_, err = r.client.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: false})
		if err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			failures = append(failures, fmt.Errorf("remove bind volume: %w", err))
		}
	}
	return errors.Join(failures...)
}
