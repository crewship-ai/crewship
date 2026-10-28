package api

import (
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPageCatalogManagementFiltersAfterAuthorityBeforePagination(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	pagesSeedUser(t, h, ws, "catalog-reader", "catalog-reader@example.test", "MEMBER")
	pagesJoinCrew(t, h, "catalog-reader", "crew-lookout")
	for _, slug := range []string{"a-normal", "b-test", "c-archived", "d-test-archived", "e-hidden-test", "codex-test-name-only"} {
		row := pagesCreate(t, h, ws, owner, slug)
		marked := slug == "b-test" || slug == "d-test-archived" || slug == "e-hidden-test"
		archived := ""
		if slug == "c-archived" || slug == "d-test-archived" {
			archived = "2026-10-28T19:34:11Z"
		}
		execOrFatal(t, h.db, `UPDATE pages SET created_by_test=?,archived_at=NULLIF(?,''),updated_at='2026-10-28T19:34:11Z' WHERE id=?`, marked, archived, row["id"])
		if slug == "e-hidden-test" {
			execOrFatal(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, row["id"])
		}
	}
	for _, tc := range []struct {
		query  string
		count  int
		slugs  []string
		status int
	}{
		{"", 2, []string{"a-normal", "codex-test-name-only"}, 200},
		{"test_items=only", 1, []string{"b-test"}, 200},
		{"archived=only", 1, []string{"c-archived"}, 200},
		{"test_items=only&archived=only", 1, []string{"d-test-archived"}, 200},
		{"test_items=include&archived=include&limit=1&offset=3", 5, []string{"codex-test-name-only"}, 200},
		{"test_items=include&archived=include&audience=personal", 0, nil, 200},
		{"test_items=invalid", 0, nil, 400}, {"archived=invalid", 0, nil, 400},
	} {
		t.Run(tc.query, func(t *testing.T) {
			req := pagesRequest(t, "GET", "/api/v1/pages?"+tc.query, ws, "catalog-reader", "MEMBER", "")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status%d: %s", rr.Code, rr.Body)
			}
			if tc.status != 200 {
				return
			}
			var rows []pageListWire
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if rr.Header().Get("X-Total-Count") != strconv.Itoa(tc.count) || len(rows) != len(tc.slugs) {
				t.Fatalf("count%s rows%s", rr.Header().Get("X-Total-Count"), rr.Body)
			}
			for i, row := range rows {
				if row.Slug != tc.slugs[i] {
					t.Fatalf("row%s want%s", row.Slug, tc.slugs[i])
				}
			}
			if strings.Contains(rr.Body.String(), "e-hidden-test") {
				t.Fatal("private metadata leaked")
			}
		})
	}
	// The catalog is not an access grant or a read prohibition.
	rec, err := h.loadPage(t.Context(), ws, "d-test-archived")
	if err != nil || !rec.CreatedByTest || rec.ArchivedAt == "" {
		t.Fatalf("direct retained metadata: %#v %v", rec, err)
	}
}

func TestPageCatalogHumanCreateMarkerAndCreateOnly(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	body := strings.Replace(pagesSpecBody("explicit-test"), "{", `{"created_by_test":true,`, 1)
	req := pagesRequest(t, http.MethodPost, "/api/v1/pages", ws, owner, "OWNER", body)
	req = req.WithContext(withAuthKind(req.Context(), AuthKindCLIToken))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create%d %s", rr.Code, rr.Body)
	}
	var row pageWire
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if !row.CreatedByTest || row.Archived || row.ArchivedAt != "" {
		t.Fatalf("metadata%#v", row)
	}
	rec, err := h.loadPage(t.Context(), ws, "explicit-test")
	if err != nil || !rec.CreatedByTest {
		t.Fatalf("persisted %#v %v", rec, err)
	}
	update := pagesRequest(t, http.MethodPut, "/api/v1/pages/explicit-test", ws, owner, "OWNER", `{"created_by_test":false}`)
	update.SetPathValue("slug", "explicit-test")
	denied := httptest.NewRecorder()
	h.Update(denied, update)
	if denied.Code != 400 {
		t.Fatalf("update%d %s", denied.Code, denied.Body)
	}
	agentBody := strings.Replace(body, "explicit-test", "agent-test", 1)
	agentReq := pagesRequest(t, http.MethodPost, "/api/v1/pages", ws, owner, "OWNER", agentBody)
	agentReq.Header.Set("X-Internal-Token", "untrusted-test-origin")
	refused := httptest.NewRecorder()
	h.Create(refused, agentReq)
	if refused.Code != 403 {
		t.Fatalf("agent%d %s", refused.Code, refused.Body)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='agent-test'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("denied side effect%d %v", n, err)
	}
}

func TestPageCatalogMarkerSurvivesPortableImportWithoutArchive(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	row := pagesCreate(t, h, ws, owner, "marked-source")
	execOrFatal(t, h.db, `UPDATE pages SET created_by_test=1,archived_at='2026-10-28T19:34:11Z' WHERE id=?`, row["id"])
	bundle, _ := pagesExport(t, h, ws, owner, "marked-source")
	meta, ok := bundle["metadata"].(map[string]any)
	if !ok || meta["created_by_test"] != true {
		t.Fatalf("export lost marker %#v", bundle)
	}
	bundle["slug"] = "marked-copy"
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	req := pagesRequest(t, "POST", "/api/v1/pages/import", ws, owner, "OWNER", string(raw))
	req = req.WithContext(withAuthKind(req.Context(), AuthKindCLIToken))
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != 201 {
		t.Fatalf("import%d %s", rr.Code, rr.Body)
	}
	rec, err := h.loadPage(t.Context(), ws, "marked-copy")
	if err != nil || !rec.CreatedByTest || rec.ArchivedAt != "" {
		t.Fatalf("copy %#v %v", rec, err)
	}
}

func TestArtifactProjectTestMarkerIsHumanOnlyAndRetainedAcrossCodeVersions(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := `{"slug":"three-screens","name":"Test item","created_by_test":true,` + strings.TrimPrefix(artifactProjectBody(0, "v1"), "{")
	created := call(owner, "POST", "artifacts", body)
	if created.Code != 201 {
		t.Fatalf("create%d %s", created.Code, created.Body)
	}
	for _, op := range []struct{ path, body string }{
		{"three-screens/artifact/versions", artifactProjectBody(1, "v2")},
		{"three-screens/artifact/restore", `{"base_version":2,"version":1}`},
		{"three-screens/artifact/share", `{"base_version":3,"version":2}`},
	} {
		rr := call(owner, "POST", op.path, op.body)
		if rr.Code != 200 {
			t.Fatalf("%s:%d %s", op.path, rr.Code, rr.Body)
		}
	}
	rec, err := h.loadPage(t.Context(), ws, "three-screens")
	if err != nil || !rec.CreatedByTest {
		t.Fatalf("retained%#v %v", rec, err)
	}
	deniedBody := strings.Replace(body, "three-screens", "agent-marker", 1)
	req := pagesRequest(t, "POST", "/api/v1/pages/artifacts", ws, owner, "OWNER", deniedBody)
	req = req.WithContext(context.WithValue(req.Context(), artifactRunKey{}, &artifactRunAuthority{}))
	denied := httptest.NewRecorder()
	h.CreateArtifactProject(denied, req)
	if denied.Code != 403 {
		t.Fatalf("untrusted agent marker%d %s", denied.Code, denied.Body)
	}
}

func TestPageCatalogFolderCountsUseSameVisibilityFilter(t *testing.T) {
	f := newFoldersFixture(t)
	folder := f.createFolder(t, "catalog-folder", "Catalog folder", "crew/engine")
	for _, slug := range []string{"normal", "marked", "archived"} {
		row := pagesCreate(t, f.h, f.wsID, f.owner, slug)
		archived := ""
		if slug == "archived" {
			archived = "2026-10-28T19:34:11Z"
		}
		execOrFatal(t, f.h.db, `UPDATE pages SET folder_id=?,created_by_test=?,archived_at=NULLIF(?, '') WHERE id=?`, folder["id"], slug == "marked", archived, row["id"])
	}
	for _, tc := range []struct {
		q     string
		count int
	}{{"", 1}, {"test_items=only", 1}, {"archived=only", 1}, {"test_items=include&archived=include", 3}} {
		req := pagesRequest(t, "GET", "/api/v1/page-folders/catalog-folder?"+tc.q, f.wsID, f.owner, "OWNER", "")
		req.SetPathValue("slug", "catalog-folder")
		rr := httptest.NewRecorder()
		f.h.GetFolder(rr, req)
		if rr.Code != 200 {
			t.Fatalf("get %s:%d %s", tc.q, rr.Code, rr.Body)
		}
		var detail pageFolderShowWire
		if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.PageCount != tc.count || len(detail.Pages) != tc.count {
			t.Fatalf("get %s count%d rows%d", tc.q, detail.PageCount, len(detail.Pages))
		}
		listReq := pagesRequest(t, "GET", "/api/v1/page-folders?"+tc.q, f.wsID, f.owner, "OWNER", "")
		list := httptest.NewRecorder()
		f.h.ListFolders(list, listReq)
		if list.Code != 200 {
			t.Fatalf("list%d %s", list.Code, list.Body)
		}
		var listed struct {
			Folders []pageFolderWire `json:"folders"`
		}
		if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range listed.Folders {
			if row.Slug == "catalog-folder" {
				found = true
				if row.PageCount != tc.count {
					t.Fatalf("list %s count%d", tc.q, row.PageCount)
				}
			}
		}
		if !found {
			t.Fatal("folder missing")
		}
	}
}

func TestPageCatalogMigrationLeavesLegacyRowsUnmarked(t *testing.T) {
	db := testutil.NewMemDB(t)
	execOrFatal(t, db, `CREATE TABLE pages(id TEXT PRIMARY KEY)`)
	execOrFatal(t, db, `INSERT INTO pages(id) VALUES('legacy-codex-test')`)
	migration, err := os.ReadFile("../database/migrations/20261028193411_page_catalog_management.sql")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, string(migration))
	var marker int
	var unarchived bool
	if err := db.QueryRow(`SELECT created_by_test,archived_at IS NULL FROM pages WHERE id='legacy-codex-test'`).Scan(&marker, &unarchived); err != nil || marker != 0 || !unarchived {
		t.Fatalf("legacy changed %d %t %v", marker, unarchived, err)
	}
	if _, err := db.Exec(`UPDATE pages SET created_by_test=2`); err == nil {
		t.Fatal("invalid marker bypassed CHECK")
	}
	if _, err := db.Exec(`UPDATE pages SET created_by_test=NULL`); err == nil {
		t.Fatal("null marker bypassed NOT NULL")
	}
}

func TestPageCatalogWorkbookAndMooringCreateBindTestOrigin(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	for _, tc := range []struct{ slug, path, body string }{
		{"test-book", "/api/v1/workbooks?workspace_id=ws-old", `{"slug":"test-book","name":"Test book","created_by_test":true}`},
		{"test-mooring", "/api/v1/mooring/documents?workspace_id=ws-old", `{"slug":"test-mooring","name":"Test mooring","app_id":"hello","created_by_test":true}`},
	} {
		first := f.do(f.boss, "POST", tc.path, tc.body)
		wantCode(t, first, 201, "marked human create")
		var marked bool
		if err := f.db.QueryRow(`SELECT created_by_test FROM pages WHERE slug=?`, tc.slug).Scan(&marked); err != nil || !marked {
			t.Fatalf("marker %t %v", marked, err)
		}
		wantCode(t, f.do(f.boss, "POST", tc.path, tc.body), 200, "identical marked retry")
		wantCode(t, f.do(f.boss, "POST", tc.path, strings.Replace(tc.body, `"created_by_test":true`, `"created_by_test":false`, 1)), 409, "retry may not clear marker")
	}
}
