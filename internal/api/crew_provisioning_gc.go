package api

// Orphan-resource garbage collection: stale temp containers from
// crashed provision runs, plus unreferenced crewship-cache:* images.
// Extracted from crew_provisioning.go for readability.

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// without standing up a real Docker daemon. Satisfied by *docker.Client.
type orphanGCClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
}

// ProvisioningHandler provides endpoints for the devcontainer feature catalog

const (
	tempContainerMaxAge = 1 * time.Hour
	orphanGCInterval    = 30 * time.Minute
	orphanGCSweepCap    = 200 // defensive — don't stall on pathological state

	// cacheImageMinAge is a safety window: a crewship-cache:* image younger
	// than this is skipped by the orphan sweeper even if no crew row points
	// at it. Rationale — Provision() writes the DB row AFTER `docker commit`.
	// Between those two steps (seconds at most) the image legitimately looks
	// "unreferenced". A 5-minute floor is many orders of magnitude larger
	// than the actual race window, at zero operational cost.
	cacheImageMinAge = 5 * time.Minute

	// cacheGCAutoDeleteEnv gates destructive removal of unreferenced
	// crewship-cache:* images. Default (unset/false) is log-only — an operator
	// has to opt in to deletion because dropping an image someone just built
	// locally is surprising.
	cacheGCAutoDeleteEnv = "CREWSHIP_CACHE_GC_AUTODELETE"
)

// runStartupAndPeriodicGC performs one sweep at process startup and then

func (h *ProvisioningHandler) runStartupAndPeriodicGC(ctx context.Context) {
	// Startup sweep — tolerate failures (Docker may not yet be reachable).
	h.sweepOrphanTempContainers(ctx)
	h.sweepOrphanCacheImages(ctx)

	ticker := time.NewTicker(orphanGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sweepOrphanTempContainers(ctx)
			h.sweepOrphanCacheImages(ctx)
		}
	}
}

// sweepOrphanTempContainers removes temp containers created by the provisioner
// that have outlived a full provisioning run (tempContainerMaxAge). A normal
// run cleans up via defer; this sweeper only catches the crash/SIGKILL path.
// Filtered by the Provisioner's label so we never touch unrelated containers.

func (h *ProvisioningHandler) sweepOrphanTempContainers(ctx context.Context) {
	if h.gcClient == nil {
		return
	}
	start := time.Now()
	labelFilter := devcontainer.TempContainerLabelKey + "=" + devcontainer.TempContainerLabelValue
	containers, err := h.gcClient.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelFilter)),
	})
	if err != nil {
		h.logger.Warn("orphan temp-container GC: list failed", "error", err)
		return
	}
	cutoff := time.Now().Add(-tempContainerMaxAge).Unix()
	removed := 0
	for i, c := range containers {
		if i >= orphanGCSweepCap {
			h.logger.Warn("orphan temp-container GC: sweep cap hit; remaining containers skipped",
				"cap", orphanGCSweepCap, "total", len(containers))
			break
		}
		if c.Created >= cutoff {
			continue
		}
		if err := h.gcClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			h.logger.Warn("orphan temp-container GC: remove failed", "container", c.ID, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		h.logger.Info("orphan temp-container GC: removed stale temp containers",
			"removed", removed, "scanned", len(containers), "duration", time.Since(start))
	} else {
		h.logger.Debug("orphan temp-container GC: nothing to remove",
			"scanned", len(containers), "duration", time.Since(start))
	}
}

// sweepOrphanCacheImages finds crewship-cache:* images that have no referencing
// crew row across ALL workspaces. These are leaks from a crash window between
// ContainerCommit and the crews.cached_image UPDATE. Removal is opt-in via
// CREWSHIP_CACHE_GC_AUTODELETE=true — default is log-only for visibility.

func (h *ProvisioningHandler) sweepOrphanCacheImages(ctx context.Context) {
	if h.gcClient == nil {
		return
	}
	// 1. Collect every cached_image still referenced by any crew across all
	//    workspaces (no workspace filter — an image referenced by another
	//    tenant's crew must never be deleted).
	rows, err := h.db.QueryContext(ctx,
		`SELECT DISTINCT cached_image FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != ''
		       AND deleted_at IS NULL`)
	if err != nil {
		h.logger.Warn("orphan cache-image GC: query failed", "error", err)
		return
	}
	defer rows.Close()
	referenced := make(map[string]struct{})
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			h.logger.Warn("orphan cache-image GC: scan failed", "error", err)
			return
		}
		referenced[tag] = struct{}{}
	}
	// Critical: if iteration died mid-stream, `referenced` is incomplete and
	// using it to decide orphans could delete a still-live cache image.
	if err := rows.Err(); err != nil {
		h.logger.Warn("orphan cache-image GC: rows iteration failed", "error", err)
		return
	}

	// 2. Compare against the local image set. Any crewship-cache:* tag with
	//    no referencing row AND older than cacheImageMinAge is an orphan.
	//    The age floor closes the race between ContainerCommit and the DB
	//    UPDATE inside Provision() — a freshly-committed image legitimately
	//    looks "unreferenced" until the caller persists the link.
	imgs, err := h.listLocalImagesCached(ctx)
	if err != nil {
		h.logger.Warn("orphan cache-image GC: image list failed", "error", err)
		return
	}
	autoDelete := strings.EqualFold(os.Getenv(cacheGCAutoDeleteEnv), "true") ||
		os.Getenv(cacheGCAutoDeleteEnv) == "1"

	safeCutoff := time.Now().Add(-cacheImageMinAge).Unix()
	orphans := make([]string, 0)
	tooYoung := 0
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if !strings.HasPrefix(tag, cacheImagePrefix) {
				continue
			}
			if _, ok := referenced[tag]; ok {
				continue
			}
			if img.Created > safeCutoff {
				tooYoung++
				continue
			}
			orphans = append(orphans, tag)
		}
	}
	if len(orphans) == 0 {
		h.logger.Debug("orphan cache-image GC: nothing to report", "skipped_too_young", tooYoung)
		return
	}
	if !autoDelete {
		h.logger.Info("orphan cache-image GC: unreferenced cache images detected (log-only, set CREWSHIP_CACHE_GC_AUTODELETE=true to remove)",
			"orphans", orphans, "count", len(orphans), "skipped_too_young", tooYoung)
		return
	}
	removed := 0
	for _, tag := range orphans {
		if _, err := h.gcClient.ImageRemove(ctx, tag, image.RemoveOptions{Force: false, PruneChildren: true}); err != nil {
			h.logger.Warn("orphan cache-image GC: remove failed", "tag", tag, "error", err)
			continue
		}
		removed++
	}
	h.logger.Info("orphan cache-image GC: removed unreferenced cache images",
		"removed", removed, "total_orphans", len(orphans), "skipped_too_young", tooYoung)
}

// cleanupOldJobs removes completed/failed jobs older than 1h from the jobs map.
// Called periodically from the provisioning handler lifetime.
