package api

import (
	"bytes"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pageassets"
)

func pageAssetPNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 2, 3))
	im.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func uploadAsset(t *testing.T, h *PageHandler, ws, user, slug, mutation string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/api/v1/pages/"+slug+"/assets?mutation_id="+mutation, ws, user, "OWNER", string(raw))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	h.UploadPageAsset(w, r)
	return w
}
func assetMeta(t *testing.T, w *httptest.ResponseRecorder) pageAssetMetadata {
	t.Helper()
	var a pageAssetMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func getAsset(t *testing.T, h *PageHandler, ws, user, slug, id string, body bool) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "GET", "/api/v1/pages/"+slug+"/assets/"+id, ws, user, "OWNER", "")
	r.SetPathValue("slug", slug)
	r.SetPathValue("assetId", id)
	w := httptest.NewRecorder()
	if body {
		h.GetPageAssetBytes(w, r)
	} else {
		h.GetPageAsset(w, r)
	}
	return w
}
func TestPageAssetUploadReadIdempotencyAndAuthority(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	raw := pageAssetPNG(t)
	w := uploadAsset(t, h, ws, user, "report", "one", raw)
	artifactExpect(t, w, 201)
	a := assetMeta(t, w)
	if a.PageID != page || a.State != "staged" || a.Width != 2 || a.Height != 3 || !strings.HasPrefix(a.ID, "c") {
		t.Fatalf("metadata=%+v", a)
	}
	expiry, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if err != nil || time.Until(expiry) < 23*time.Hour {
		t.Fatalf("expiry=%s %v", a.ExpiresAt, err)
	}
	replay := uploadAsset(t, h, ws, user, "report", "one", raw)
	artifactExpect(t, replay, 200)
	if assetMeta(t, replay).ID != a.ID {
		t.Fatal("retry changed identity")
	}
	artifactExpect(t, uploadAsset(t, h, ws, user, "report", "one", []byte("bad")), 422)
	before := assetBlobFiles(t, h.artifactStoragePath)
	var different bytes.Buffer
	_ = png.Encode(&different, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	artifactExpect(t, uploadAsset(t, h, ws, user, "report", "one", different.Bytes()), 409)
	if assetBlobFiles(t, h.artifactStoragePath) != before {
		t.Fatal("mutation refusal wrote blob")
	}
	got := getAsset(t, h, ws, user, "report", a.ID, true)
	artifactExpect(t, got, 200)
	if got.Header().Get("Content-Type") != "image/png" || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe read headers")
	}
	im, err := png.Decode(bytes.NewReader(got.Body.Bytes()))
	if err != nil || im.Bounds().Dy() != 3 {
		t.Fatal("canonical bytes invalid")
	}
	artifactExpect(t, getAsset(t, h, ws, user, "report", a.SHA256, false), 404)
	artifactExpect(t, getAsset(t, h, ws, user, "missing", a.ID, true), 404)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('other-asset-page',?,'other-assets','Other',?,'{}')`, ws, user)
	artifactExpect(t, getAsset(t, h, ws, user, "other-assets", a.ID, true), 404)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user)
	artifactExpect(t, getAsset(t, h, ws, user, "report", a.ID, true), 403)
}
func TestPageAssetQuotaExpiryAndGCRoot(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	a := assetMeta(t, uploadAsset(t, h, ws, user, "report", "original", pageAssetPNG(t)))
	for i := 1; i < 64; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_assets SELECT ? ,page_id,sha256,content_type,width,height,byte_size,author_user_id,?,request_digest,state,created_at,expires_at FROM page_assets WHERE id=?`, generateCUID(), generateCUID(), a.ID)
	}
	before := assetBlobFiles(t, h.artifactStoragePath)
	for i := 0; i < 5; i++ {
		var b bytes.Buffer
		im := image.NewRGBA(image.Rect(0, 0, 3+i, 3))
		_ = png.Encode(&b, im)
		artifactExpect(t, uploadAsset(t, h, ws, user, "report", generateCUID(), b.Bytes()), 409)
	}
	if got := assetBlobFiles(t, h.artifactStoragePath); got != before {
		t.Fatalf("quota refusal wrote orphan blobs: before%d after%d", before, got)
	}
	unlock := lockAttachmentBlob(ws, a.SHA256)
	free, err := attachmentBlobIsUnreferenced(t.Context(), h.db, ws, a.SHA256)
	unlock()
	if err != nil || free {
		t.Fatalf("asset GC root lost %v", err)
	}
	// No synthetic revision references: this checks the staging state boundary only.
	execOrFatal(t, h.db, `DELETE FROM page_assets WHERE id<>?`, a.ID)
	for _, state := range []string{"staged", "retained"} {
		execOrFatal(t, h.db, `INSERT INTO page_assets(id,page_id,sha256,content_type,width,height,byte_size,author_user_id,mutation_id,request_digest,state,created_at,expires_at) SELECT ?,page_id,sha256,content_type,width,height,byte_size,author_user_id,?,request_digest,?,'2020-01-01T00:00:00.000Z','2020-01-02T00:00:00.000Z' FROM page_assets WHERE id=?`, state, state, state, a.ID)
	}
	runAttachmentGCPass(t.Context(), h.db, h.logger, h.artifactStoragePath)
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_assets WHERE page_id=?`, page).Scan(&count); err != nil || count != 2 {
		t.Fatalf("staging expiry count=%d err=%v", count, err)
	}
	artifactExpect(t, getAsset(t, h, ws, user, "report", "staged", false), 404)
	artifactExpect(t, getAsset(t, h, ws, user, "report", "retained", true), 200)
	if _, err := h.db.Exec(`UPDATE page_assets SET state='staged' WHERE id='retained'`); err == nil {
		t.Fatal("retained reverted to expiring")
	}
	if _, err := h.db.Exec(`UPDATE page_assets SET sha256='tampered' WHERE id=?`, a.ID); err == nil {
		t.Fatal("asset identity mutable")
	}
}
func TestPageAssetACLRecheckedAfterDecode(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	raw := pageAssetPNG(t)
	raster, err := pageassets.Canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	unlock := lockAttachmentBlob(ws, attachmentDigest(raster.Bytes))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- uploadAsset(t, h, ws, user, "report", "revoked", raw) }()
	deadline := time.Now().Add(3 * time.Second)
	for len(pageAssetDecodeSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(pageAssetDecodeSlots) == 0 {
		unlock()
		t.Fatal("upload did not acquire decode slot")
	}
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user)
	unlock()
	artifactExpect(t, <-done, 403)
	var n int
	_ = h.db.QueryRow(`SELECT count(*) FROM page_assets`).Scan(&n)
	if n != 0 {
		t.Fatal("revoked upload published")
	}
}
func TestPageAssetBackupForkBytes(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	a := assetMeta(t, uploadAsset(t, h, ws, user, "report", "backup", pageAssetPNG(t)))
	const pass = "page-assets-backup-test-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	db := setupTestDB(t)
	root := storageDir(t)
	restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: "asset-fork", Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	var id, page, sha string
	if err = db.QueryRow(`SELECT a.id,a.page_id,a.sha256 FROM page_assets a JOIN pages p ON p.id=a.page_id WHERE p.workspace_id=?`, restored.RestoredWorkspaceID).Scan(&id, &page, &sha); err != nil {
		t.Fatal(err)
	}
	if id == a.ID || page == a.PageID || sha != a.SHA256 {
		t.Fatalf("fork identity %s %s %s", id, page, sha)
	}
	raw, err := readAttachmentBlob(root, restored.RestoredWorkspaceID, sha)
	if err != nil || attachmentDigest(raw) != sha {
		t.Fatalf("fork bytes %v", err)
	}
}

func assetBlobFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && len(d.Name()) == 64 {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestPageAssetPanelReachAndCrossWorkspace(t *testing.T) {
	h, _, ws, user, _ := pagesGrantFixture(t, "")
	h.artifactStoragePath = storageDir(t)
	w := uploadAsset(t, h, ws, user, "fleet-201", "panel", pageAssetPNG(t))
	artifactExpect(t, w, 201)
	a := assetMeta(t, w)
	pagesSeedUser(t, h, ws, "asset-stoker", "asset-stoker@example.invalid", "MEMBER")
	execOrFatal(t, h.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('asset-stoker-member','crew-engine','asset-stoker')`)
	artifactExpect(t, getAsset(t, h, ws, "asset-stoker", "fleet-201", a.ID, true), 200)
	execOrFatal(t, h.db, `DELETE FROM crew_members WHERE id='asset-stoker-member'`)
	artifactExpect(t, getAsset(t, h, ws, "asset-stoker", "fleet-201", a.ID, true), 404)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('asset-foreign-ws','Foreign','asset-foreign')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('asset-foreign-member','asset-foreign-ws',?,'OWNER')`, user)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('asset-foreign-page','asset-foreign-ws','fleet-201','Foreign',?,'{}')`, user)
	artifactExpect(t, getAsset(t, h, "asset-foreign-ws", user, "fleet-201", a.ID, true), 404)
}

func TestPageAssetMissingCorruptAndMalformed(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	for _, raw := range [][]byte{[]byte("not an image"), []byte(`<svg><script/></svg>`), pageAssetPNG(t)[:30], make([]byte, pageassets.MaxInput+1)} {
		before := assetBlobFiles(t, h.artifactStoragePath)
		w := uploadAsset(t, h, ws, user, "report", generateCUID(), raw)
		if w.Code != 422 && w.Code != 413 {
			t.Fatalf("malformed status%d", w.Code)
		}
		if assetBlobFiles(t, h.artifactStoragePath) != before {
			t.Fatal("invalid input wrote blob")
		}
	}
	w := uploadAsset(t, h, ws, user, "report", "corrupt", pageAssetPNG(t))
	artifactExpect(t, w, 201)
	a := assetMeta(t, w)
	path, err := attachmentBlobPath(h.artifactStoragePath, ws, a.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, getAsset(t, h, ws, user, "report", a.ID, true), 503)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, getAsset(t, h, ws, user, "report", a.ID, true), 503)
}

func TestPageAssetRealRouterAuthentication(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('router-asset-page',?,'asset-router','Assets',?,'{}')`, workspace, user)
	const secret = "synthetic-page-assets-router-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, user, "asset-router@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithStoragePath(storageDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	call := func(method, path string, raw []byte, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	endpoint := "/api/v1/pages/asset-router/assets?mutation_id=router&workspace_id=" + workspace
	artifactExpect(t, call(http.MethodPost, endpoint, pageAssetPNG(t), false), 401)
	created := call(http.MethodPost, endpoint, pageAssetPNG(t), true)
	artifactExpect(t, created, 201)
	a := assetMeta(t, created)
	path := "/api/v1/pages/asset-router/assets/" + a.ID + "/bytes?workspace_id=" + workspace
	artifactExpect(t, call(http.MethodGet, path, nil, false), 401)
	got := call(http.MethodGet, path, nil, true)
	artifactExpect(t, got, 200)
	if _, err = png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, call(http.MethodGet, "/api/v1/pages/asset-router/assets/"+a.ID+"/other?workspace_id="+workspace, nil, true), 404)
}

func TestPageAssetRequiresMutationID(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	for _, id := range []string{"", "bad.dot", strings.Repeat("a", 129)} {
		w := uploadAsset(t, h, ws, user, "report", id, pageAssetPNG(t))
		artifactExpect(t, w, 400)
		if assetBlobFiles(t, h.artifactStoragePath) != 0 {
			t.Fatal("invalid identity published bytes")
		}
	}
}
func TestPageAssetRetryIsActorBound(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	raw := pageAssetPNG(t)
	artifactExpect(t, uploadAsset(t, h, ws, user, "report", "actor-bound", raw), 201)
	pagesSeedUser(t, h, ws, "asset-other-admin", "asset-other-admin@example.invalid", "ADMIN")
	before := assetBlobFiles(t, h.artifactStoragePath)
	artifactExpect(t, uploadAsset(t, h, ws, "asset-other-admin", "report", "actor-bound", raw), 409)
	if assetBlobFiles(t, h.artifactStoragePath) != before {
		t.Fatal("different actor retry published bytes")
	}
}
