package api

// crewship-cache:* image inventory + the local-image-list memoization
// shared with the orphan sweeper. Owns CacheList / CacheDelete public
// handlers. Extracted from crew_provisioning.go.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// cacheArtifactReferences joins immutable IDs and legacy tag references. An
// image may have several aliases; all describe the same underlying artifact.
func cacheArtifactReferences(refs map[string][]string, img image.Summary) []string {
	crews := append([]string{}, refs[img.ID]...)
	for _, tag := range img.RepoTags {
		crews = append(crews, refs[devcontainer.CanonicalCacheRef(tag)]...)
	}
	slices.Sort(crews)
	return slices.Compact(crews)
}

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

// cacheDeleteGateBudget bounds the work a manual cache delete does while it
// holds the image-use gate exclusively.
const cacheDeleteGateBudget = 15 * time.Second

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
		key := devcontainer.CanonicalCacheRef(tag)
		refs[key] = append(refs[key], slug)
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
		replyInternalError(w, h.logger, "query referenced cache images", err)
		return
	}
	defer refRows.Close()
	refs := make(map[string][]string)
	for refRows.Next() {
		var tag, slug string
		if err := refRows.Scan(&tag, &slug); err != nil {
			replyInternalError(w, h.logger, "scan referenced cache image", err)
			return
		}
		key := devcontainer.CanonicalCacheRef(tag)
		refs[key] = append(refs[key], slug)
	}

	imgs, err := h.listLocalImagesCached(r.Context())
	if err != nil {
		replyInternalError(w, h.logger, "docker image list", err)
		return
	}
	out := make([]CacheImageInfo, 0, len(imgs))
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			tag = devcontainer.CanonicalCacheRef(tag)
			if !strings.HasPrefix(tag, cacheImagePrefix) {
				continue
			}
			out = append(out, CacheImageInfo{
				Tag:          tag,
				Size:         img.Size,
				CreatedAt:    img.Created,
				ReferencedBy: cacheArtifactReferences(refs, img),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out})
}

// listLocalImagesCached returns the local Docker image set with a short-lived
// memoization. Callers must treat the slice as read-only.

func (h *ProvisioningHandler) listLocalImagesCached(ctx context.Context) ([]image.Summary, error) {
	// Fast path: cache hit under RLock. With a 10 s TTL the vast majority
	// of admin-UI polls land here, so they shouldn't serialize on each
	// other or on an unrelated invalidate.
	h.imgListMu.RLock()
	if h.imgListCache.images != nil && time.Since(h.imgListCache.fetchedAt) < imageListCacheTTL {
		cached := h.imgListCache.images
		h.imgListMu.RUnlock()
		return cached, nil
	}
	h.imgListMu.RUnlock()

	// Miss: take the write lock and re-check. Concurrent misses queue
	// here, but only the first one calls Docker — the rest find the
	// cache fresh on re-check and return immediately.
	h.imgListMu.Lock()
	defer h.imgListMu.Unlock()
	if h.imgListCache.images != nil && time.Since(h.imgListCache.fetchedAt) < imageListCacheTTL {
		return h.imgListCache.images, nil
	}
	listResult, err := h.gcClient.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	imgs := listResult.Items
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

	releaseImages, idle := resourcelifecycle.Images(h.db).TryCollect()
	if !idle {
		replyError(w, http.StatusConflict, "environment acquisition or restore is active; retry image deletion later")
		return
	}
	defer releaseImages()
	// The exclusive gate holds back crew create, update, onboarding and
	// restore. Bound the daemon and database work done under it, so a wedged
	// daemon costs this request, not every crew edit.
	ctx, cancel := context.WithTimeout(r.Context(), cacheDeleteGateBudget)
	defer cancel()
	releaseArtifact, available := resourcelifecycle.Images(h.db).TryCollectImages(tag)
	if !available {
		replyError(w, http.StatusConflict, "this image is being acquired or built; retry deletion later")
		return
	}
	defer releaseArtifact()
	// Output tags and immutable base IDs are both acquisition keys.
	inventory, inventoryErr := h.gcClient.ImageList(ctx, client.ImageListOptions{All: true})
	if inventoryErr != nil {
		replyInternalError(w, h.logger, "resolve image deletion target", inventoryErr)
		return
	}
	var candidateID string
	var candidateRefs []string
	for _, img := range inventory.Items {
		if slices.ContainsFunc(img.RepoTags, func(alias string) bool { return devcontainer.CanonicalCacheRef(alias) == tag }) {
			candidateID = img.ID
			seen := map[string]bool{tag: true, "": true}
			for _, ref := range append([]string{img.ID, devcontainer.ImageAcquisitionRef(img.Labels)}, img.RepoTags...) {
				ref = devcontainer.CanonicalCacheRef(ref)
				if !seen[ref] {
					candidateRefs = append(candidateRefs, ref)
					seen[ref] = true
				}
			}
			break
		}
	}
	if candidateID != "" {
		releaseID, available := resourcelifecycle.Images(h.db).TryCollectImages(candidateRefs...)
		if !available {
			replyError(w, http.StatusConflict, "this image is in use by a build; retry deletion later")
			return
		}
		defer releaseID()
	}

	force := r.URL.Query().Get("force") == "true"
	removeRef := tag
	// A forced delete removes the named tag, as it always has. Forcing by
	// image ID would drop every other alias of the artifact too, including
	// a crewship-env/* tag a restore published.
	if !force && dockerutil.IsLocalImageID(candidateID) {
		removeRef = candidateID
	}

	if !force {
		refs, err := h.referencedCacheImages(ctx)
		if err != nil {
			replyInternalError(w, h.logger, "query referenced cache images", err)
			return
		}
		if crews, ok := refs[tag]; ok && len(crews) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":         "image is referenced by live crews; pass ?force=true to delete anyway",
				"referenced_by": crews,
			})
			return
		}
		// A tag can point at an artifact pinned by ID. Resolve against a fresh
		// daemon list, not the UI memoization; stale aliases must not hide use.
		hasPinned := false
		for ref := range refs {
			hasPinned = hasPinned || dockerutil.IsLocalImageID(ref)
		}
		if hasPinned {
			images := inventory

			found := false
			for _, img := range images.Items {
				if !slices.ContainsFunc(img.RepoTags, func(alias string) bool { return devcontainer.CanonicalCacheRef(alias) == tag }) {
					continue
				}
				found = true
				if !dockerutil.IsLocalImageID(img.ID) {
					replyError(w, http.StatusConflict, "cached image identity could not be established")
					return
				}
				if crews := cacheArtifactReferences(refs, img); len(crews) > 0 {
					writeJSON(w, http.StatusConflict, map[string]any{
						"error":         "image is referenced by live crews; pass ?force=true to delete anyway",
						"referenced_by": crews,
					})
					return
				}
				removeRef = img.ID // do not follow a tag moved after this check
			}
			if !found {
				replyError(w, http.StatusNotFound, "cached image not found")
				return
			}
		}
	}

	// Use the narrow gcClient interface — same underlying *client.Client, but
	// keeps the destructive surface aligned with the orphan sweeper for both
	// readability and test parity.
	_, err := h.gcClient.ImageRemove(ctx, removeRef, client.ImageRemoveOptions{Force: force, PruneChildren: true})
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
