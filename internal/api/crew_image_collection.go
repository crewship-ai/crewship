package api

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const imageGCEnv = "CREWSHIP_IMAGE_GC"
const ownedImageMinAge = 24 * time.Hour

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
	release, ok := resourcelifecycle.Images(h.db).TryCollect()
	if !ok {
		return
	} // an acquisition, publication or restore is in progress
	defer release()
	if ctx.Err() != nil {
		return
	}
	scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	enabled := true
	if value, configured := os.LookupEnv(imageGCEnv); configured {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		enabled = err == nil && parsed
		if err != nil {
			h.logger.Warn("image collection disabled: invalid boolean setting", "setting", imageGCEnv)
		}
	}
	if devcontainer.ImageNamespace(h.instanceID) != "" && enabled {
		h.collectOwnedImages(scanCtx)
	}
	// Preserve the explicit operational escape hatch for historical images.
	if strings.EqualFold(os.Getenv(cacheGCAutoDeleteEnv), "true") || os.Getenv(cacheGCAutoDeleteEnv) == "1" || h.instanceID == "" {
		h.sweepLegacyCacheImages(scanCtx)
	}
}

func (h *ProvisioningHandler) collectOwnedImages(ctx context.Context) {
	// Inventory all containers, including stopped containers and foreign owners.
	containers, err := h.gcClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		h.logger.Warn("image collection: container inventory unavailable", "error", err)
		return
	}
	used := map[string]bool{}
	for _, c := range containers.Items {
		if hasTempContainerName(c.Names) {
			return
		}
		if !dockerutil.IsLocalImageID(c.ImageID) {
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
			return
		}
		byRef[img.ID] = img.ID
		for _, tag := range img.RepoTags {
			byRef[tag] = img.ID
		}
	}
	protected, err := h.protectedImageIDs(ctx, byRef)
	if err != nil {
		h.logger.Warn("image collection: references unavailable; retaining images", "error", err)
		return
	}
	cutoff := time.Now().Add(-ownedImageMinAge).Unix()
	for _, img := range listed.Items {
		if ctx.Err() != nil {
			return
		}
		if !ownedImage(img, h.instanceID) || img.Created <= 0 || img.Created > cutoff || used[img.ID] || protected[img.ID] {
			continue
		}
		// Do not recursively prune parents: ownership is established only for this ID.
		if _, err := h.gcClient.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{Force: false, PruneChildren: false}); err != nil {
			h.logger.Info("image collection: artifact retained", "image_id", img.ID, "error", err)
			continue
		}
		h.invalidateImageListCache()
		h.InvalidateImageCache()
		h.logger.Info("image collection: removed unused owned artifact", "image_id", img.ID)
	}
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
	for _, tag := range img.RepoTags {
		if tag == "<none>:<none>" {
			continue
		}
		if !strings.HasPrefix(tag, prefix+instanceID+"-") {
			return false
		}
	}
	return true
}

// Current and previous selections protect bytes; immutable build history is
// provenance, not the sequence of cache re-selections.
func (h *ProvisioningHandler) protectedImageIDs(ctx context.Context, byRef map[string]string) (map[string]bool, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT COALESCE(cached_image,''),COALESCE(last_selected_image,''),COALESCE(previous_image,'') FROM crews WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	protected := map[string]bool{}
	for rows.Next() {
		var current, last, previous string
		if err := rows.Scan(&current, &last, &previous); err != nil {
			return nil, err
		}
		for _, ref := range []string{current, last, previous} {
			if ref == "" {
				continue
			}
			id := byRef[ref]
			if id == "" {
				// A complete inventory proves an immutable ID absent. An unresolved
				// mutable selection cannot be attributed safely, so retain everything.
				if dockerutil.IsLocalImageID(ref) {
					protected[ref] = true
					continue
				}
				return nil, sql.ErrNoRows
			}
			protected[id] = true
		}
	}
	return protected, rows.Err()
}
