package api

// crewship-cache:* image inventory + the local-image-list memoization
// shared with the orphan sweeper. Owns CacheList / CacheDelete public
// handlers. Extracted from crew_provisioning.go.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"
)

type cachedImageList struct {
	images    []image.Summary
	fetchedAt time.Time
}

// imageListCacheTTL is short on purpose: cache images mutate only via our
// own Provision (which we can't invalidate from here without coupling) and
// CacheDelete (which we DO invalidate). The TTL bounds the staleness window

// for the admin UI while still cutting the common-case poll cost.
const imageListCacheTTL = 10 * time.Second

// Rate limit constants. Per-workspace bucket.
//
// maxConcurrentProvisionsPerWorkspace must be at least as large as the number
// of crews a fresh seed creates (currently 4) so `crewship seed` can fire all
// trigger requests without any of them blocking behind the rate limiter. We
// picked 8 as a modest ceiling above the seed count — high enough for demo
// and small-team workspaces to provision everything in parallel, low enough

const cacheImagePrefix = "crewship-cache:"

// CacheImageInfo describes a cached devcontainer image for the CLI/UI.

type CacheImageInfo struct {
	Tag          string   `json:"tag"`
	Size         int64    `json:"size"`
	CreatedAt    int64    `json:"created_at"` // Unix seconds (Docker image.Summary.Created is int64).
	ReferencedBy []string `json:"referenced_by"`
}

// referencedCacheImages returns the set of cached_image tags currently
// referenced by live (non-deleted) crews, with the list of crew slugs that
// reference each tag. Used by both the list and prune paths to prevent
// deleting an image a crew still depends on.

func (h *ProvisioningHandler) referencedCacheImages(ctx context.Context) (map[string][]string, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT cached_image, slug FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != '' AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make(map[string][]string)
	for rows.Next() {
		var tag, slug string
		if err := rows.Scan(&tag, &slug); err != nil {
			return nil, err
		}
		refs[tag] = append(refs[tag], slug)
	}
	return refs, rows.Err()
}

// CacheList returns metadata for every crewship-cache:* image on the host,
// annotated with the list of crew slugs referencing it.
//
// Workspace scoping: the image store is host-global (Docker has no concept
// of workspaces), so this endpoint returns all cache images visible to the
// daemon. The referenced_by field is filtered to crews in the requester's
// workspace, matching how other provisioning endpoints behave.

func (h *ProvisioningHandler) CacheList(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cache management not available (Docker client not configured)",
		})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())

	// Build referenced_by map scoped to this workspace.
	refRows, err := h.db.QueryContext(r.Context(),
		`SELECT cached_image, slug FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != ''
		       AND deleted_at IS NULL AND workspace_id = ?`,
		workspaceID)
	if err != nil {
		h.logger.Error("query referenced cache images", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer refRows.Close()
	refs := make(map[string][]string)
	for refRows.Next() {
		var tag, slug string
		if err := refRows.Scan(&tag, &slug); err != nil {
			h.logger.Error("scan referenced cache image", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		refs[tag] = append(refs[tag], slug)
	}

	imgs, err := h.listLocalImagesCached(r.Context())
	if err != nil {
		h.logger.Error("docker image list", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	out := make([]CacheImageInfo, 0, len(imgs))
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if !strings.HasPrefix(tag, cacheImagePrefix) {
				continue
			}
			out = append(out, CacheImageInfo{
				Tag:          tag,
				Size:         img.Size,
				CreatedAt:    img.Created,
				ReferencedBy: refs[tag],
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out})
}

// listLocalImagesCached returns the local Docker image set with a short-lived
// memoization. Callers must treat the slice as read-only.

func (h *ProvisioningHandler) listLocalImagesCached(ctx context.Context) ([]image.Summary, error) {
	h.imgListMu.Lock()
	defer h.imgListMu.Unlock()

	if h.imgListCache.images != nil && time.Since(h.imgListCache.fetchedAt) < imageListCacheTTL {
		return h.imgListCache.images, nil
	}
	imgs, err := h.gcClient.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	h.imgListCache = cachedImageList{images: imgs, fetchedAt: time.Now()}
	return imgs, nil
}

// invalidateImageListCache forces the next listLocalImagesCached call to hit
// Docker. Called after CacheDelete; Provision can't reach us from the
// devcontainer package without coupling, so we rely on the TTL for that path.

func (h *ProvisioningHandler) invalidateImageListCache() {
	h.imgListMu.Lock()
	h.imgListCache = cachedImageList{}
	h.imgListMu.Unlock()
}

// CacheDelete removes a single crewship-cache:* image. Refuses if the image
// is referenced by any crew (across all workspaces, not just the caller's —
// we never want to delete a live crew's cache from another workspace).
// Query param ?force=true bypasses the referenced check.

func (h *ProvisioningHandler) CacheDelete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "delete") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cache management not available (Docker client not configured)",
		})
		return
	}

	tag := r.PathValue("tag")
	if tag == "" {
		replyError(w, http.StatusBadRequest, "tag is required")
		return
	}
	// Hard-enforce the crewship-cache namespace — we never delete an
	// arbitrary Docker image on behalf of a caller.
	if !strings.HasPrefix(tag, cacheImagePrefix) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "only crewship-cache:* tags may be deleted",
		})
		return
	}

	force := r.URL.Query().Get("force") == "true"

	if !force {
		refs, err := h.referencedCacheImages(r.Context())
		if err != nil {
			h.logger.Error("query referenced cache images", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if crews, ok := refs[tag]; ok && len(crews) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":         "image is referenced by live crews; pass ?force=true to delete anyway",
				"referenced_by": crews,
			})
			return
		}
	}

	// Use the narrow gcClient interface — same underlying *client.Client, but
	// keeps the destructive surface aligned with the orphan sweeper for both
	// readability and test parity.
	_, err := h.gcClient.ImageRemove(r.Context(), tag, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		h.logger.Error("docker image remove", "tag", tag, "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to remove cached image")
		return
	}
	// Cached image list no longer reflects local state.
	h.invalidateImageListCache()
	writeJSON(w, http.StatusOK, map[string]string{"tag": tag, "status": "deleted"})
}

// isEmptyRequirements reports whether an AggregatedRequirements value has no
// runtime customizations. Used to store NULL instead of "{}" in the DB so the
// absence of requirements is trivially distinguishable at query time.
