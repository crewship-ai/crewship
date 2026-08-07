package docker

// One-time migration of pre-#1732 sidecar resources onto the id-scoped naming.
//
// Before #1732 a crew's sidecar container was "<prefix>-svc-<slug>-<service>"
// and its volumes "<prefix>-svc-<slug>-vol-<volume>" — slug only, no crew id,
// on a daemon shared by every workspace. Upgrading to the id-scoped names
// without touching what is already on the daemon would be the WORSE of the two
// bugs, in two distinct ways:
//
//   - The legacy postgres keeps running. It holds the `postgres` alias on the
//     crew bridge, and so does the new id-scoped one, so the agent's DNS lookup
//     round-robins between its real database and a freshly-initialised empty
//     one. Intermittent, and it looks like data corruption.
//   - The legacy data volume is left with nothing referencing it. The data is
//     still on disk but no crew can reach it again — the same data loss as a
//     delete, by a slower route.
//
// So we migrate, using exactly the mechanism audit C1 already established for
// the crew's own home/tools volumes (migrateLegacyCrewResources):
//
//   - the legacy CONTAINER is stopped and removed. A sidecar is ephemeral
//     compute; its state lives in the volumes, and the ensure loop recreates it
//     under the id-scoped name moments later.
//   - the legacy VOLUME's data is copied into the id-scoped volume by a
//     short-lived helper, and the legacy volume is pruned only after the copy
//     exits 0. A copy that cannot complete is fail-safe: the legacy volume is
//     left untouched and the error is returned, which pauses the crew's start
//     rather than starting it against an empty database.
//
// Two crews that share a slug across workspaces both see the same legacy
// resources — the ambiguity this whole issue is about. It is resolved the same
// way C1 resolved it: serialized on the legacy slug, first crew to provision
// claims the data, the other gets a clean volume, and the log says so. There
// is no better answer available after the fact; the point of the id-scoped
// names is that it can never arise again.

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// legacySidecarVolumeMigration is one volume's worth of pending work: copy
// `legacy` into `target`, then label the target as `volName`'s.
type legacySidecarVolumeMigration struct {
	legacy  string
	target  string
	volName string
}

// migrateLegacySidecarResources reconciles the crew's pre-#1732 sidecar
// container(s) and volume(s) onto the id-scoped scheme. No-op on a daemon that
// has none. Called from EnsureCrewServices while the caller holds the crew's
// id-scoped lock.
func (p *Provider) migrateLegacySidecarResources(ctx context.Context, crewID, crewSlug string, services []provider.CrewService) error {
	if crewID == "" || crewSlug == "" || len(services) == 0 {
		return nil
	}

	// Serialize by *legacy slug*, not crew id: the resources reconciled below
	// are slug-scoped, so two crews sharing a slug would otherwise both observe
	// the same legacy volumes and race to copy/claim the same data. The key is
	// namespaced away from the C1 crew-resource migration's own slug lock so
	// the two never contend with each other. The caller already holds the
	// id-scoped crew lock; this nested slug lock is deadlock-free because
	// crew-id locks are independent and acquired first.
	mu := p.lockForMigration("svc:" + crewSlug)
	mu.Lock()
	defer mu.Unlock()

	containers, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers (legacy sidecar migration): %w", err)
	}

	// Enumerate volumes only when some service actually declares one. A crew
	// whose sidecars are all stateless has no volume to strand, and asking the
	// daemon for the full volume list on every crew start is pure cost.
	legacyVolumes := map[string]bool{}
	existingVolumes := map[string]bool{}
	if anyServiceDeclaresAVolume(services) {
		// Fail closed if volumes can't be enumerated, for the same reason the
		// C1 migration does: we cannot tell whether an unmigrated legacy data
		// volume is sitting here, and the ensure loop would go on to create a
		// fresh empty id-scoped volume that strands it behind an
		// authoritative-looking target no future start will re-migrate.
		volList, listErr := p.client.VolumeList(ctx, volumeListOptions())
		if listErr != nil {
			return fmt.Errorf("list volumes (legacy sidecar migration): %w; "+
				"legacy sidecar data was NOT removed and this crew's services are not started, "+
				"so no empty id-scoped volume strands it — retry once the daemon can enumerate volumes again", listErr)
		}
		for _, vol := range volList.Items {
			existingVolumes[vol.Name] = true
			// A volume carrying a crew-id label was created by a post-#1732
			// binary and is somebody's LIVE id-scoped volume — never a
			// migration source. This matters because slugs may contain
			// hyphens: crew "alpha-<id>"'s legacy volume name is
			// byte-identical to crew "alpha"/id "<id>"'s id-scoped one. The
			// label distinguishes them where the name cannot.
			if vol.Labels[crewCrewIDLabel] == "" {
				legacyVolumes[vol.Name] = true
			}
		}
	}

	for i := range services {
		svc := &services[i]

		// Work out what this service actually has to migrate before pulling
		// anything: the copy helper needs the image locally, and pulling on
		// every crew start when there is nothing to migrate would be a pointless
		// registry round trip.
		pending := make([]legacySidecarVolumeMigration, 0, len(svc.Volumes))
		for _, vol := range svc.Volumes {
			legacy := p.legacySidecarVolumeName(crewSlug, vol.Name)
			target := p.sidecarVolumeName(crewID, crewSlug, vol.Name)
			if legacy == target || !legacyVolumes[legacy] {
				continue // already migrated, never existed, or is a live id-scoped volume
			}
			if existingVolumes[target] {
				// Both names exist — do not clobber the id-scoped data. Leave the
				// legacy volume in place; an operator can prune it once they
				// confirm it is stale.
				p.logger.Warn("legacy slug-scoped sidecar volume orphaned (#1732 migration): target id-scoped volume already exists, leaving legacy in place — operator may prune it",
					"legacy_volume", legacy, "target_volume", target, "crew_id", crewID, "service", svc.Name)
				continue
			}
			pending = append(pending, legacySidecarVolumeMigration{legacy: legacy, target: target, volName: vol.Name})
		}

		removedLegacyContainer, err := p.removeLegacySidecarContainer(ctx, containers.Items, crewSlug, svc.Name)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			if removedLegacyContainer {
				p.logger.Info("removed legacy slug-scoped sidecar container (#1732 migration); it will be recreated under its id-scoped name",
					"crew_id", crewID, "crew_slug", crewSlug, "service", svc.Name)
			}
			continue
		}

		// The copy helper runs the sidecar's OWN image: it is the one image
		// guaranteed to be appropriate for this data, and the ensure loop is
		// about to pull it anyway. Best-effort pull (tolerates a registry
		// outage when a local copy exists), same policy as ensureSidecar.
		if err := p.pullSidecarImage(ctx, svc.Image); err != nil {
			return fmt.Errorf("legacy sidecar volume migration for service %q needs image %q to run the copy helper "+
				"and it could not be pulled: %w; the legacy volume was NOT removed — its data is intact", svc.Name, svc.Image, err)
		}
		for _, m := range pending {
			if err := p.migrateLegacyVolumeLabeled(ctx, m.legacy, m.target, svc.Image,
				sidecarVolumeLabels(crewID, crewSlug, svc.Name, m.volName)); err != nil {
				return fmt.Errorf("legacy sidecar volume migration (service %q): %w", svc.Name, err)
			}
			p.logger.Info("migrated legacy slug-scoped sidecar volume (#1732 migration)",
				"legacy_volume", m.legacy, "target_volume", m.target, "crew_id", crewID, "service", svc.Name,
				"note", "if this crew's slug was shared across workspaces before #1732, the migrated data may be ambiguous — the first crew to start claims it")
		}
	}
	return nil
}

// anyServiceDeclaresAVolume reports whether the migration has any reason to
// enumerate the daemon's volumes.
func anyServiceDeclaresAVolume(services []provider.CrewService) bool {
	for i := range services {
		if len(services[i].Volumes) > 0 {
			return true
		}
	}
	return false
}

// removeLegacySidecarContainer stops and force-removes the crew's pre-#1732
// slug-only sidecar container for one service, if the daemon has one. Reports
// whether it removed anything.
//
// A container carrying a crewship.crew-id label is skipped: it was created by a
// post-#1732 binary and belongs to whichever crew that label names. The check
// is not paranoia — slugs may contain hyphens, so crew "alpha-<id>"'s legacy
// container name is byte-identical to crew "alpha"/id "<id>"'s id-scoped one,
// and matching on the name alone would have one crew's start delete another
// crew's running database.
func (p *Provider) removeLegacySidecarContainer(ctx context.Context, items []container.Summary, crewSlug, serviceName string) (bool, error) {
	legacyName := "/" + p.legacySidecarContainerName(crewSlug, serviceName)
	for _, c := range items {
		if c.Labels[crewCrewIDLabel] != "" {
			continue
		}
		matched := false
		for _, n := range c.Names {
			if n == legacyName {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		timeout := 10
		_, _ = p.client.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &timeout})
		if _, err := p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
			return false, fmt.Errorf("remove legacy slug-scoped sidecar container %q (#1732 migration): %w",
				p.legacySidecarContainerName(crewSlug, serviceName), err)
		}
		return true, nil
	}
	return false, nil
}
