package api

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const imageGCEnv = "CREWSHIP_IMAGE_GC"
const ownedImageMinAge = 24 * time.Hour

// ownedImageUnusedWindow is how long an owned image must have stayed unused
// and unprotected, as this installation observed it, before it is removed.
// The image's age alone is not a grace period: an image built days ago and
// deselected a minute ago would otherwise go on the next pass (#5398). The
// first observation is recorded in resource_retention_images; a use or
// protection seen at a pass restarts the window. Zero removes at first sight.
//
// Passes are the only observations. A use shorter than the pass interval is
// not seen, and when passes stop for a while (collection switched off, the
// server down) nothing is seen at all, so after a gap longer than
// ownedImageObservationGap every window starts over instead of being trusted.
var ownedImageUnusedWindow = 24 * time.Hour

// ownedImageObservationGap is the longest silence between two completed
// passes after which earlier observations still count. Four pass intervals:
// a restart or a few deferred passes do not reset the windows, a day with
// collection off does.
const ownedImageObservationGap = 4 * orphanGCInterval

// lastPassMarker is the image_id of the row that records when the last pass
// completed for an installation. It can never be an image ID.
const lastPassMarker = "last-completed-pass"

// deletedCrewImageRetention keeps a soft-deleted crew's selections protected
// for this long after the deletion, so reviving it (the onboarding setup crew
// is revived routinely) does not silently rebuild its environment (#5398).
const deletedCrewImageRetention = 7 * 24 * time.Hour

// One image collector for the installation. The legacy opt-in is restricted
// to unlabelled artifacts; shared BuildKit records and base images are excluded.
func (h *ProvisioningHandler) sweepOrphanCacheImages(ctx context.Context) {
	if h.gcClient == nil || ctx.Err() != nil {
		return
	}
	writer, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	defer writer.Leave()
	ctx = writer.Context()
	scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h.mu.RLock()
	enabled := h.legacyImageEviction
	h.mu.RUnlock()
	if value, configured := os.LookupEnv(imageGCEnv); configured {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		enabled = err == nil && parsed
		if err != nil {
			h.logger.Warn("image collection disabled: invalid boolean setting", "setting", imageGCEnv)
		}
	}
	h.mu.RLock()
	instanceID := h.instanceID
	h.mu.RUnlock()
	if devcontainer.ImageNamespace(instanceID) != "" && enabled {
		h.collectOwnedImages(scanCtx)
	}
	// Always report historical backlog; explicit legacy deletion remains opt-in.
	h.sweepLegacyCacheImages(scanCtx)

}

func (h *ProvisioningHandler) collectOwnedImages(ctx context.Context) {
	h.mu.RLock()
	owner := h.instanceID
	h.mu.RUnlock()
	unusedSince, err := h.ownedImagesUnusedSince(ctx, owner)
	if err != nil {
		h.logger.Warn("image collection: unused-image state unavailable; retaining images", "error", err)
		return
	}
	now := time.Now()
	if lastPass, known := unusedSince[lastPassMarker]; !known || now.Sub(lastPass) > ownedImageObservationGap || lastPass.After(now) {
		// Nobody was watching: an image may have been used and released
		// unseen. Start every window over rather than act on what was true
		// before the gap.
		if len(unusedSince) > 0 {
			if _, err := h.db.ExecContext(ctx, `DELETE FROM resource_retention_images WHERE instance_id=?`, owner); err != nil {
				h.logger.Warn("image collection: unused-image state reset failed; retaining images", "error", err)
				return
			}
		}
		unusedSince = map[string]time.Time{}
	}
	delete(unusedSince, lastPassMarker)
	// Revoke the previous completion marker before observing any use. A
	// failed state update (or a crash mid-pass) must force a fresh window next
	// time, even when the previous completed pass was less than two hours ago.
	if _, err := h.db.ExecContext(ctx, `DELETE FROM resource_retention_images WHERE instance_id=? AND image_id=?`, owner, lastPassMarker); err != nil {
		h.logger.Warn("image collection: cannot begin durable observation; retaining images", "error", err)
		return
	}

	// Inventory all containers, including stopped containers and foreign owners.
	containers, err := h.gcClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		h.logger.Warn("image collection: container inventory unavailable", "error", err)
		return
	}
	used := map[string]bool{}
	for _, c := range containers.Items {
		if hasTempContainerName(c.Names) {
			h.logger.Debug("image collection: provisioning container source retained", "container_id", c.ID)
		}
		if !dockerutil.IsLocalImageID(c.ImageID) {
			h.logger.Warn("image collection deferred: incomplete container identity", "container_id", c.ID)
			return
		} // incomplete observation
		used[c.ImageID] = true
	}
	listed, err := h.gcClient.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		h.logger.Warn("image collection: image inventory unavailable", "error", err)
		return
	}
	byRef := map[string]string{}
	for _, img := range listed.Items {
		if !dockerutil.IsLocalImageID(img.ID) {
			h.logger.Warn("image collection deferred: incomplete image identity")
			return
		}
		byRef[img.ID] = img.ID
		for _, tag := range img.RepoTags {
			byRef[tag] = img.ID
		}
	}
	protected, err := h.protectedImageIDs(ctx, byRef, true)
	if err != nil {
		h.logger.Warn("image collection: references unavailable; retaining images", "error", err)
		return
	}
	cutoff := time.Now().Add(-ownedImageMinAge).Unix()
	present := map[string]bool{}
	for _, img := range listed.Items {
		if ctx.Err() != nil {
			return
		}
		if !ownedImage(img, owner) {
			continue
		}
		present[img.ID] = true
		if used[img.ID] || protected[img.ID] {
			// Any observed use restarts the window.
			if _, tracked := unusedSince[img.ID]; tracked {
				if err := h.forgetOwnedImageUnused(ctx, owner, img.ID); err != nil {
					return
				}
			}
			continue
		}
		if img.Created <= 0 || img.Created > cutoff {
			continue
		}
		if ownedImageUnusedWindow > 0 {
			first, tracked := unusedSince[img.ID]
			if !tracked {
				if _, err := h.db.ExecContext(ctx, `INSERT INTO resource_retention_images(instance_id,image_id,first_unused_at) VALUES(?,?,?) ON CONFLICT(instance_id,image_id) DO NOTHING`,
					owner, img.ID, tsformat.Format(now)); err != nil {
					h.logger.Warn("image collection: unused-image state write failed", "error", err)
					return
				}
				continue
			}
			if now.Sub(first) < ownedImageUnusedWindow {
				continue
			}
		}
		if err := h.removeCollectedImage(ctx, img, true); err != nil {
			return
		}
	}
	for id := range unusedSince {
		if ctx.Err() != nil {
			return
		}
		if !present[id] {
			if err := h.forgetOwnedImageUnused(ctx, owner, id); err != nil {
				return
			}
		}
	}
	// Only a pass that looked at everything counts as an observation.
	if ctx.Err() != nil {
		return
	}
	if _, err := h.db.ExecContext(ctx, `INSERT INTO resource_retention_images(instance_id,image_id,first_unused_at) VALUES(?,?,?)
		ON CONFLICT(instance_id,image_id) DO UPDATE SET first_unused_at=excluded.first_unused_at`,
		owner, lastPassMarker, tsformat.Format(now)); err != nil {
		h.logger.Warn("image collection: pass marker write failed", "error", err)
	}
}

// ownedImagesUnusedSince reads when this installation first observed each
// owned image unused, plus the lastPassMarker row. A row whose time cannot be
// read is deleted, so that image's window starts over at this pass.
func (h *ProvisioningHandler) ownedImagesUnusedSince(ctx context.Context, owner string) (map[string]time.Time, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT image_id, first_unused_at FROM resource_retention_images WHERE instance_id=?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	var unreadable []string
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, at) // tsformat:allow: parse only; the value is written with tsformat.Format and compared as time.Time in Go
		if err != nil {
			unreadable = append(unreadable, id)
			continue
		}
		out[id] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for _, id := range unreadable {
		if err := h.forgetOwnedImageUnused(ctx, owner, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h *ProvisioningHandler) forgetOwnedImageUnused(ctx context.Context, owner, imageID string) error {
	if _, err := h.db.ExecContext(ctx, `DELETE FROM resource_retention_images WHERE instance_id=? AND image_id=?`, owner, imageID); err != nil {
		h.logger.Warn("image collection: unused-image state write failed", "error", err)
		return err
	}
	return nil
}

func ownedImage(img image.Summary, instanceID string) bool {
	if img.Labels[devcontainer.ImageInstanceLabel] != instanceID {
		return false
	}
	kind := img.Labels[devcontainer.ImageKindLabel]
	if kind != devcontainer.ImageKindCrew && kind != devcontainer.ImageKindFeature {
		return false
	}
	// A foreign alias makes provenance ambiguous; untagged images are recognized
	// through explicit full owner labels. Reserved-label spoofing by Docker
	// administrators is outside the managed lifecycle contract.
	prefix := "crewship-cache:"
	if kind == devcontainer.ImageKindFeature {
		prefix = devcontainer.FeatureImageTagPrefix
	}
	tagged := false
	for _, tag := range img.RepoTags {
		if tag == "<none>:<none>" {
			continue
		}
		if !strings.HasPrefix(tag, prefix+instanceID+"-") {
			return false
		}
		tagged = true
	}
	// An untagged artifact must still reserve its canonical build alias.
	// Otherwise a cached BuildKit export can reacquire this ID under that
	// alias after the final inventory while collection leases only the ID.
	return tagged || devcontainer.ImageAcquisitionRef(img.Labels) != ""
}

// Current and previous selections protect bytes; immutable build history is
// provenance, not the sequence of cache re-selections.
func (h *ProvisioningHandler) protectedImageIDs(ctx context.Context, byRef map[string]string, report bool) (map[string]bool, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT COALESCE(cached_image,''),COALESCE(last_selected_image,''),COALESCE(previous_image,''),COALESCE(runtime_image,''),COALESCE(devcontainer_config,''),COALESCE(deleted_at,'') FROM crews`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	protected := map[string]bool{}
	for rows.Next() {
		var current, last, previous, base, config, deletedAt string
		if err := rows.Scan(&current, &last, &previous, &base, &config, &deletedAt); err != nil {
			return nil, err
		}
		// A soft-deleted crew keeps its selections for a while: it can be
		// revived. A deletion time that cannot be read protects, never frees.
		if deletedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, deletedAt) // tsformat:allow: parse only; deleted_at is read and compared as time.Time in Go
			if err == nil && time.Since(at) > deletedCrewImageRetention {
				continue
			}
		}
		// Protect configured bases even before a live crew has a container.
		if id := byRef[base]; id != "" {
			protected[id] = true
		}
		if config != "" {
			cfg, err := devcontainer.ParseBytes([]byte(config))
			if err != nil {
				if report {
					h.logger.Warn("image collection: invalid crew configuration; explicit selections remain protected")
				}
			} else if id := byRef[cfg.Image]; id != "" {
				protected[id] = true
			}
		}
		for _, ref := range []string{current, last, previous} {
			if ref == "" {
				continue
			}
			id := byRef[ref]
			if id == "" {
				if report {
					h.logger.Warn("image collection: selected reference absent from complete inventory; skipping reference", "image_ref", ref)
				}
				continue
			}

			protected[id] = true
		}
	}
	return protected, rows.Err()
}

// Inventory is outside the lifecycle gate. Final checks/removal have a small
// deadline and exclude only this artifact's build aliases (and rare restores).
func (h *ProvisioningHandler) removeCollectedImage(ctx context.Context, candidate image.Summary, owned bool) error {
	gate := resourcelifecycle.Images(h.db)
	release, ok := gate.TryCollect()
	if !ok {
		h.logger.Info("image collection deferred: restore or crew edit active")
		return nil
	}
	defer release()
	refs := append([]string{candidate.ID, devcontainer.ImageAcquisitionRef(candidate.Labels)}, candidate.RepoTags...)
	releaseImage, ok := gate.TryCollectImages(refs...)
	if !ok {
		h.logger.Info("image collection: candidate build active; retaining artifact", "image_id", candidate.ID)
		if owned {
			return h.forgetOwnedImageUnused(ctx, h.imageCollectionOwner(), candidate.ID)
		}
		return nil
	}
	defer releaseImage()
	finalCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	listed, err := h.gcClient.ImageList(finalCtx, client.ImageListOptions{All: true})
	if err != nil {
		h.logger.Warn("image collection deferred: final inventory unavailable", "error", err)
		return nil
	}
	byRef := map[string]string{}
	var current *image.Summary
	for i := range listed.Items {
		img := &listed.Items[i]
		if owned && !dockerutil.IsLocalImageID(img.ID) {
			h.logger.Warn("image collection deferred: final inventory incomplete")
			return nil
		}
		byRef[img.ID] = img.ID
		for _, tag := range img.RepoTags {
			byRef[tag] = img.ID
		}
		if img.ID == candidate.ID {
			current = img
		}
	}
	if current == nil {
		h.logger.Info("image collection: candidate already absent", "image_id", candidate.ID)
		if owned {
			return h.forgetOwnedImageUnused(ctx, h.imageCollectionOwner(), candidate.ID)
		}
		return nil
	}
	if !slices.Equal(candidate.RepoTags, current.RepoTags) {
		h.logger.Info("image collection: aliases changed; retry next pass", "image_id", candidate.ID)
		return nil
	}
	h.mu.RLock()
	owner := h.instanceID
	h.mu.RUnlock()
	if owned && !ownedImage(*current, owner) {
		h.logger.Warn("image collection: ownership changed; retaining artifact", "image_id", candidate.ID)
		return nil
	}
	if !owned && current.Labels[devcontainer.ImageInstanceLabel] != "" {
		return nil
	}
	protected, err := h.protectedImageIDs(finalCtx, byRef, false)
	if err != nil {
		h.logger.Warn("image collection deferred: final references unavailable", "error", err)
		return nil
	}
	if protected[candidate.ID] {
		if owned {
			return h.forgetOwnedImageUnused(ctx, owner, candidate.ID)
		}
		return nil
	}
	if _, err := h.gcClient.ImageRemove(finalCtx, candidate.ID, client.ImageRemoveOptions{Force: false, PruneChildren: false}); err != nil {
		h.logger.Info("image collection: artifact retained", "image_id", candidate.ID, "error", err)
		if owned {
			return h.forgetOwnedImageUnused(ctx, owner, candidate.ID)
		}
		return nil
	}
	h.invalidateImageListCache()
	h.InvalidateImageCache()
	h.logger.Info("image collection: removed unused artifact", "image_id", candidate.ID)
	if owned {
		// The same ID can come back (a reproducible rebuild, a restore). It
		// must not inherit this window. The pass context may be spent.
		forgetCtx, cancelForget := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelForget()
		return h.forgetOwnedImageUnused(forgetCtx, owner, candidate.ID)
	}
	return nil
}

func (h *ProvisioningHandler) imageCollectionOwner() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.instanceID
}
