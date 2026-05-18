package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
)

// ---------------------------------------------------------------------------
// crew_provisioning_cache.go — CacheList / CacheDelete / referencedCacheImages
//
// Critical paths covered:
//   - RBAC: role gates on read (CacheList) and delete (CacheDelete)
//   - docker-nil → 503 service-unavailable on both handlers
//   - tag validation: missing, wrong prefix
//   - referenced-by scoping: CacheList filters to caller's workspace;
//     CacheDelete uses cross-workspace check (any live crew blocks deletion)
//   - force=true bypasses reference check
//   - image list cache invalidation after delete
// ---------------------------------------------------------------------------

// newCacheTestHandler builds a ProvisioningHandler suitable for testing the
// cache endpoints. docker is set to a non-nil sentinel client (NewClientWithOpts
// does not dial) so the != nil guard passes; all real Docker calls are routed
// through the fake gcClient.
func newCacheTestHandler(t *testing.T, fake orphanGCClient) *ProvisioningHandler {
	t.Helper()
	dc, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("init docker client sentinel: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	return &ProvisioningHandler{
		db:       setupTestDB(t),
		logger:   newTestLogger(),
		docker:   dc,
		gcClient: fake,
	}
}

func TestCacheList_Forbidden_NoRole(t *testing.T) {
	h := newCacheTestHandler(t, &fakeGCClient{})
	req := httptest.NewRequest("GET", "/api/v1/cache", nil)
	// no role context → canRole returns false
	rr := httptest.NewRecorder()
	h.CacheList(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("no-role status = %d, want 403", rr.Code)
	}
}

func TestCacheList_DockerNil_503(t *testing.T) {
	h := &ProvisioningHandler{
		db:       setupTestDB(t),
		logger:   newTestLogger(),
		docker:   nil,
		gcClient: &fakeGCClient{},
	}
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("GET", "/api/v1/cache", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheList(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("docker-nil status = %d, want 503", rr.Code)
	}
}

func TestCacheList_FiltersAndWorkspaceScopedReferences(t *testing.T) {
	fake := &fakeGCClient{
		images: []image.Summary{
			{RepoTags: []string{"crewship-cache:abc123"}, Size: 12345, Created: 1700000000},
			{RepoTags: []string{"crewship-cache:def456"}, Size: 99, Created: 1700000100},
			// Non-cache image — must be filtered out
			{RepoTags: []string{"ghcr.io/foo/bar:latest"}, Size: 1, Created: 1700000200},
			// Image with multiple tags — only crewship-cache tag must appear
			{RepoTags: []string{"alpine:3", "crewship-cache:multi"}, Size: 7, Created: 1700000300},
		},
	}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	// Different workspace whose crew references one of our cache images.
	otherWS := "ws-other"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	// In-workspace crews referencing cache images
	seedCrewRow(t, h.db, "c1", wsID, "Crew One", "crew-one")
	seedCrewRow(t, h.db, "c2", wsID, "Crew Two", "crew-two")
	// Cross-workspace crew referencing 'abc123' — must NOT appear in
	// referenced_by for this caller. This is the privacy contract.
	seedCrewRow(t, h.db, "c-other", otherWS, "Other Crew", "other-crew")
	// Deleted crew — must NOT appear even though it references the image
	seedCrewRow(t, h.db, "c-deleted", wsID, "Gone", "gone")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = ? WHERE id = ?`, "crewship-cache:abc123", "c1"); err != nil {
		t.Fatalf("attach cached_image: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = ? WHERE id = ?`, "crewship-cache:abc123", "c2"); err != nil {
		t.Fatalf("attach cached_image c2: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = ? WHERE id = ?`, "crewship-cache:abc123", "c-other"); err != nil {
		t.Fatalf("attach cached_image c-other: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = ?, deleted_at = '2024-01-01' WHERE id = ?`, "crewship-cache:abc123", "c-deleted"); err != nil {
		t.Fatalf("attach cached_image c-deleted: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/cache", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}

	var body struct {
		Images []CacheImageInfo `json:"images"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Expect exactly 3 crewship-cache:* tags (abc123, def456, multi).
	gotTags := make([]string, 0, len(body.Images))
	for _, im := range body.Images {
		gotTags = append(gotTags, im.Tag)
	}
	sort.Strings(gotTags)
	wantTags := []string{"crewship-cache:abc123", "crewship-cache:def456", "crewship-cache:multi"}
	if len(gotTags) != len(wantTags) {
		t.Fatalf("got tags %v, want %v (non-cache + duplicate-tag filtering)", gotTags, wantTags)
	}
	for i, tag := range gotTags {
		if tag != wantTags[i] {
			t.Errorf("tag[%d] = %s, want %s", i, tag, wantTags[i])
		}
	}

	// Verify referenced_by for abc123 is exactly {crew-one, crew-two} — NOT
	// other-crew (cross-workspace) and NOT gone (deleted).
	for _, im := range body.Images {
		if im.Tag != "crewship-cache:abc123" {
			continue
		}
		refs := append([]string(nil), im.ReferencedBy...)
		sort.Strings(refs)
		want := []string{"crew-one", "crew-two"}
		if len(refs) != len(want) {
			t.Fatalf("abc123 referenced_by = %v, want %v (workspace-scoped, deleted excluded)", refs, want)
		}
		for i, s := range refs {
			if s != want[i] {
				t.Errorf("ref[%d] = %s, want %s", i, s, want[i])
			}
		}
	}
}

func TestCacheDelete_RBACAndValidation(t *testing.T) {
	fake := &fakeGCClient{}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	// MEMBER cannot delete — only OWNER/ADMIN
	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:x", nil)
	req.SetPathValue("tag", "crewship-cache:x")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER delete code = %d, want 403", rr.Code)
	}

	// Missing tag → 400
	req2 := httptest.NewRequest("DELETE", "/api/v1/cache/", nil)
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.CacheDelete(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("missing tag code = %d, want 400", rr2.Code)
	}

	// Non-cache prefix → 400 (defense-in-depth, never delete arbitrary images)
	req3 := httptest.NewRequest("DELETE", "/api/v1/cache/alpine:latest", nil)
	req3.SetPathValue("tag", "alpine:latest")
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rr3 := httptest.NewRecorder()
	h.CacheDelete(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Errorf("non-cache prefix code = %d, want 400", rr3.Code)
	}
	if len(fake.removedImages) != 0 {
		t.Errorf("non-cache prefix must not call ImageRemove, got %v", fake.removedImages)
	}
}

func TestCacheDelete_DockerNil_503(t *testing.T) {
	h := &ProvisioningHandler{
		db:       setupTestDB(t),
		logger:   newTestLogger(),
		docker:   nil,
		gcClient: &fakeGCClient{},
	}
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:x", nil)
	req.SetPathValue("tag", "crewship-cache:x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("docker-nil status = %d, want 503", rr.Code)
	}
}

func TestCacheDelete_RefusesReferenced_AcrossWorkspaces(t *testing.T) {
	fake := &fakeGCClient{}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	// Reference comes from a DIFFERENT workspace — still must block
	// deletion. Comment in source: "we never want to delete a live crew's
	// cache from another workspace."
	otherWS := "ws-x"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'X', 'x')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, h.db, "c-x", otherWS, "X", "crew-x")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:shared' WHERE id = 'c-x'`); err != nil {
		t.Fatalf("attach cached_image: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:shared", nil)
	req.SetPathValue("tag", "crewship-cache:shared")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("referenced delete code = %d, want 409", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 409 response: %v body=%s", err, rr.Body.String())
	}
	refs, _ := body["referenced_by"].([]any)
	if len(refs) != 1 || refs[0] != "crew-x" {
		t.Errorf("referenced_by = %v, want [crew-x]", refs)
	}
	if len(fake.removedImages) != 0 {
		t.Errorf("conflict must not call ImageRemove, got %v", fake.removedImages)
	}
}

func TestCacheDelete_ForceBypassesReferenceCheckAndInvalidatesCache(t *testing.T) {
	fake := &fakeGCClient{
		images: []image.Summary{
			{RepoTags: []string{"crewship-cache:forced"}, Created: 1700000000},
		},
	}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "c1", wsID, "C1", "crew-one")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:forced' WHERE id = 'c1'`); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Prime the image-list cache so we can verify invalidation.
	if _, err := h.listLocalImagesCached(context.Background()); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	if h.imgListCache.images == nil {
		t.Fatalf("expected cache to be primed")
	}

	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:forced?force=true", nil)
	req.SetPathValue("tag", "crewship-cache:forced")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("force delete code = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if len(fake.removedImages) != 1 || fake.removedImages[0] != "crewship-cache:forced" {
		t.Errorf("removedImages = %v, want [crewship-cache:forced]", fake.removedImages)
	}
	// Post-delete: image-list cache must be invalidated so the next List sees
	// fresh Docker state.
	if h.imgListCache.images != nil {
		t.Errorf("expected image-list cache to be invalidated after delete")
	}
}

func TestCacheDelete_UnreferencedHappyPath(t *testing.T) {
	fake := &fakeGCClient{
		images: []image.Summary{
			{RepoTags: []string{"crewship-cache:lonely"}, Created: 1700000000},
		},
	}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:lonely", nil)
	req.SetPathValue("tag", "crewship-cache:lonely")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("happy delete code = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if len(fake.removedImages) != 1 {
		t.Errorf("ImageRemove not called: removedImages=%v", fake.removedImages)
	}
}

func TestCacheDelete_ImageRemoveFailure_500(t *testing.T) {
	fake := &failingRemoveGCClient{}
	h := newCacheTestHandler(t, fake)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/cache/crewship-cache:boom", nil)
	req.SetPathValue("tag", "crewship-cache:boom")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("remove-failure code = %d, want 500", rr.Code)
	}
}

func TestReferencedCacheImages_AcrossWorkspacesIgnoresDeleted(t *testing.T) {
	h := newCacheTestHandler(t, &fakeGCClient{})
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	otherWS := "ws-cross"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Cross', 'cross')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, h.db, "live-a", wsID, "A", "alpha")
	seedCrewRow(t, h.db, "live-b", otherWS, "B", "beta")
	seedCrewRow(t, h.db, "dead", wsID, "Dead", "dead")
	_, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:shared' WHERE id IN ('live-a','live-b')`)
	if err != nil {
		t.Fatalf("attach shared: %v", err)
	}
	_, err = h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:shared', deleted_at = '2024-01-01' WHERE id = 'dead'`)
	if err != nil {
		t.Fatalf("attach deleted: %v", err)
	}

	refs, err := h.referencedCacheImages(context.Background())
	if err != nil {
		t.Fatalf("referencedCacheImages: %v", err)
	}
	got := append([]string(nil), refs["crewship-cache:shared"]...)
	sort.Strings(got)
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v (both workspaces; deleted excluded)", got, want)
	}
	for i, s := range got {
		if s != want[i] {
			t.Errorf("refs[%d] = %s, want %s", i, s, want[i])
		}
	}
}

// failingRemoveGCClient simulates ImageRemove returning an error.
type failingRemoveGCClient struct{ fakeGCClient }

func (f *failingRemoveGCClient) ImageRemove(_ context.Context, _ string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	return nil, errors.New("daemon refused")
}
