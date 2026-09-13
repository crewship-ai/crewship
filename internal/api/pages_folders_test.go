package api

// Pages folders (#2527, F1) — the acceptance scenarios of the analysis §8:
// A1 create, A2 rename, A3 delete, A4 move, A5 remove, A9 filtered read, A11
// stale fences, A14 constant statement count (in pages_list_reach_test.go).
// Every rule has its refused case in the same table as its allowed one, so a
// rule cannot be loosened without the refusal going green in the diff.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// foldersFixture is newPagesFixture plus crew/engine and a cast:
//
//	mia    MANAGER, in crew/engine       — may create and administer engine folders
//	mel    MEMBER,  in crew/engine       — a member, and only that
//	max    MANAGER, in no crew           — MANAGER+, but not the crew's
//	erin   ADMIN                         — reaches everything
//	alice  MEMBER, owns page fleet-201   — an owner with no standing on any folder
//	dave   MEMBER, read grant on fleet-201
//	frank  MEMBER, reaches nothing
//
// fleet-201's one panel is crew/lookout's, so nobody in crew/engine reaches
// it by membership: engine's folder managers cannot see the page alice owns.
type foldersFixture struct {
	h     *PageHandler
	spy   *pagesJournalSpy
	wsID  string
	owner string
}

func newFoldersFixture(t *testing.T) *foldersFixture {
	t.Helper()
	h, spy, _, wsID, ownerID := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-engine', ?, 'Engine', 'engine')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	for _, u := range []struct{ id, role string }{
		{"mia", "MANAGER"}, {"mel", "MEMBER"}, {"max", "MANAGER"}, {"erin", "ADMIN"},
		{"alice", "MEMBER"}, {"dave", "MEMBER"}, {"frank", "MEMBER"},
	} {
		pagesSeedUser(t, h, wsID, u.id, u.id+"@example.com", u.role)
	}
	pagesJoinCrew(t, h, "mia", "crew-engine")
	pagesJoinCrew(t, h, "mel", "crew-engine")
	fleet := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("fleet-201", "lookout"))
	pagesSetOwner(t, h, fleet, "alice", "")
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"dave","level":"read"}`)
	return &foldersFixture{h: h, spy: spy, wsID: wsID, owner: ownerID}
}

// call routes a request to the folder handler the method and path name.
func (f *foldersFixture) call(t *testing.T, method, path, userID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, method, path, f.wsID, userID, role, body)
	rest := strings.TrimPrefix(path, "/api/v1/page-folders")
	rest = strings.TrimPrefix(rest, "/")
	parts := strings.Split(rest, "/")
	rr := httptest.NewRecorder()
	switch {
	case rest == "" && method == "GET":
		f.h.ListFolders(rr, req)
	case rest == "" && method == "POST":
		f.h.CreateFolder(rr, req)
	case len(parts) == 1:
		req.SetPathValue("slug", parts[0])
		switch method {
		case "GET":
			f.h.GetFolder(rr, req)
		case "PATCH":
			f.h.UpdateFolder(rr, req)
		case "DELETE":
			f.h.DeleteFolder(rr, req)
		default:
			t.Fatalf("no handler for %s %s", method, path)
		}
	case len(parts) == 2 && parts[1] == "pages" && method == "POST":
		req.SetPathValue("slug", parts[0])
		f.h.AddFolderPage(rr, req)
	case len(parts) == 3 && parts[1] == "pages" && method == "DELETE":
		req.SetPathValue("slug", parts[0])
		req.SetPathValue("page", parts[2])
		f.h.RemoveFolderPage(rr, req)
	default:
		t.Fatalf("no handler for %s %s", method, path)
	}
	return rr
}

func (f *foldersFixture) createFolder(t *testing.T, slug, name, owner string) map[string]any {
	t.Helper()
	rr := f.call(t, "POST", "/api/v1/page-folders", "erin", "ADMIN",
		fmt.Sprintf(`{"slug":%q,"name":%q,"owner":%q}`, slug, name, owner))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create folder %s: %d %s", slug, rr.Code, rr.Body.String())
	}
	return decodeFoldersJSON(t, rr)
}

// file puts a page in a folder as the admin, reading the fences first the
// way a client would.
func (f *foldersFixture) file(t *testing.T, folder, page string) map[string]any {
	t.Helper()
	rr := f.call(t, "POST", "/api/v1/page-folders/"+folder+"/pages", "erin", "ADMIN", f.moveBody(t, folder, page))
	if rr.Code != http.StatusOK {
		t.Fatalf("file %s in %s: %d %s", page, folder, rr.Code, rr.Body.String())
	}
	return decodeFoldersJSON(t, rr)
}

// moveBody is the add/move request with the CURRENT fences, read as the
// admin: the page's pages_version and the folder's grants_version.
func (f *foldersFixture) moveBody(t *testing.T, folder, page string) string {
	t.Helper()
	return fmt.Sprintf(`{"page":%q,"pages_version":%d,"grants_version":%d}`, page, f.pagesVersion(t, page), f.grantsVersion(t, folder))
}

func (f *foldersFixture) pagesVersion(t *testing.T, page string) int64 {
	t.Helper()
	var v int64
	if err := f.h.db.QueryRow(`SELECT pages_version FROM pages WHERE workspace_id = ? AND slug = ?`, f.wsID, page).Scan(&v); err != nil {
		t.Fatalf("pages_version of %s: %v", page, err)
	}
	return v
}

func (f *foldersFixture) grantsVersion(t *testing.T, folder string) int64 {
	t.Helper()
	var v int64
	if err := f.h.db.QueryRow(`SELECT grants_version FROM page_folders WHERE workspace_id = ? AND slug = ?`, f.wsID, folder).Scan(&v); err != nil {
		t.Fatalf("grants_version of %s: %v", folder, err)
	}
	return v
}

func decodeFoldersJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not a JSON object: %v — %s", err, rr.Body.String())
	}
	return out
}

func expectStatus(t *testing.T, rr *httptest.ResponseRecorder, want int, contains ...string) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, want, rr.Body.String())
	}
	for _, s := range contains {
		if !strings.Contains(rr.Body.String(), s) {
			t.Errorf("body does not say %q: %s", s, rr.Body.String())
		}
	}
}

// ── A1 create ──────────────────────────────────────────────────────────────

func TestPageFolders_CreateNeedsAManagerOfTheOwningCrewOrAnAdmin(t *testing.T) {
	f := newFoldersFixture(t)
	cases := []struct {
		name   string
		user   string
		role   string
		body   string
		status int
		says   string
	}{
		{"a MANAGER who belongs to the crew creates its folder", "mia", "MANAGER",
			`{"name":"Engine ops","owner":"crew/engine","icon":"rocket","color":"amber"}`, http.StatusCreated, `"slug":"engine-ops"`},
		{"an admin outside the crew creates it too", "erin", "ADMIN",
			`{"slug":"engine-admin","name":"Admin made","owner":"crew/engine"}`, http.StatusCreated, `"owner":"crew/engine"`},
		{"a MEMBER of the crew is refused (A1)", "mel", "MEMBER",
			`{"slug":"mel","name":"Mel","owner":"crew/engine"}`, http.StatusForbidden, "MANAGER"},
		{"a MANAGER outside the crew is refused", "max", "MANAGER",
			`{"slug":"max","name":"Max","owner":"crew/engine"}`, http.StatusForbidden, "belongs to that crew"},
		{"the owner must be a crew", "erin", "ADMIN",
			`{"slug":"person","name":"Person","owner":"user/erin"}`, http.StatusBadRequest, "owned by a crew"},
		{"a crew that does not exist", "erin", "ADMIN",
			`{"slug":"ghost","name":"Ghost","owner":"crew/ghost"}`, http.StatusBadRequest, "crew/ghost does not exist"},
		{"a panel icon is not a crew icon", "erin", "ADMIN",
			`{"slug":"icon","name":"Icon","owner":"crew/engine","icon":"memory"}`, http.StatusBadRequest, "crew icon"},
		{"a hex colour is not a palette key", "erin", "ADMIN",
			`{"slug":"colour","name":"Colour","owner":"crew/engine","color":"#f59e0b"}`, http.StatusBadRequest, "amber"},
		{"a name is required", "erin", "ADMIN",
			`{"slug":"nameless","owner":"crew/engine"}`, http.StatusBadRequest, "name is required"},
		{"a bad slug is refused", "erin", "ADMIN",
			`{"slug":"Not A Slug","name":"x","owner":"crew/engine"}`, http.StatusBadRequest, "slug"},
		{"a duplicate slug is a 409", "erin", "ADMIN",
			`{"slug":"engine-ops","name":"Again","owner":"crew/engine"}`, http.StatusConflict, "already exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "POST", "/api/v1/page-folders", tc.user, tc.role, tc.body)
			expectStatus(t, rr, tc.status, tc.says)
		})
	}
	// The first case: slug derived from the name, icon and colour echoed,
	// count zero, the journal and the folder's shape.
	doc := decodeFoldersJSON(t, f.call(t, "GET", "/api/v1/page-folders/engine-ops", "mia", "MANAGER", ""))
	for k, want := range map[string]any{"slug": "engine-ops", "name": "Engine ops", "icon": "rocket", "color": "amber",
		"owner": "crew/engine", "owner_crew_name": "Engine", "page_count": float64(0), "grants_version": float64(0)} {
		if doc[k] != want {
			t.Errorf("folder %s = %v, want %v", k, doc[k], want)
		}
	}
	if pages, ok := doc["pages"].([]any); !ok || len(pages) != 0 {
		t.Errorf("pages = %v, want an empty array", doc["pages"])
	}
	e := f.spy.firstOfType(entryPageFolderChanged)
	if e == nil || e.Payload["op"] != "created" || e.Payload["folder"] != "engine-ops" || e.ActorID != "mia" {
		t.Errorf("journal: got %+v, want a page.folder_changed created entry by mia", e)
	}
}

// ── A2 rename ──────────────────────────────────────────────────────────────

func TestPageFolders_RenameIsTheOwningCrewsManagersOrAnAdmins(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	cases := []struct {
		name   string
		user   string
		role   string
		body   string
		status int
		says   string
	}{
		{"the owning crew's MANAGER renames and recolours (A2)", "mia", "MANAGER",
			`{"name":"Engine board","icon":"chart","color":"cyan"}`, http.StatusOK, `"name":"Engine board"`},
		{"a plain member of the crew is refused (A2)", "mel", "MEMBER",
			`{"name":"Mel's"}`, http.StatusForbidden, "MANAGER"},
		{"an admin outside the crew may", "erin", "ADMIN",
			`{"icon":""}`, http.StatusOK, `"icon":""`},
		{"a MANAGER with no standing and no reach gets the 404, not the rule", "max", "MANAGER",
			`{"name":"Max's"}`, http.StatusNotFound, "not found"},
		{"an empty body changes nothing", "mia", "MANAGER", `{}`, http.StatusBadRequest, "nothing to change"},
		{"the slug and owner are fixed", "mia", "MANAGER", `{"slug":"other"}`, http.StatusBadRequest, "fixed at creation"},
		{"an unknown icon is refused on update too", "mia", "MANAGER", `{"icon":"memory"}`, http.StatusBadRequest, "crew icon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "PATCH", "/api/v1/page-folders/engine-ops", tc.user, tc.role, tc.body)
			expectStatus(t, rr, tc.status, tc.says)
		})
	}
	var renamed int
	for _, e := range f.spy.entries {
		if e.Type == entryPageFolderChanged && e.Payload["op"] == "renamed" {
			renamed++
		}
	}
	if renamed != 2 {
		t.Errorf("journalled %d renames, want 2 (mia's and erin's)", renamed)
	}
}

// ── A3 delete ──────────────────────────────────────────────────────────────

func TestPageFolders_DeleteOnlyWhenEmptyCountingPagesTheCallerCannotSee(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.file(t, "engine-ops", "fleet-201")

	// mia administers the folder and cannot see the one page in it.
	if rows := pagesListRows(t, f.h, f.wsID, "mia", "MANAGER"); rows["fleet-201"] != nil {
		t.Fatal("mia can see fleet-201; the case under test needs a page she cannot")
	}
	cases := []struct {
		name   string
		user   string
		role   string
		status int
		says   string
	}{
		{"an admin cannot delete a folder with a page in it (A3)", "erin", "ADMIN", http.StatusConflict, "not empty"},
		{"nor can the crew's MANAGER, and the page she cannot see stays unnamed", "mia", "MANAGER", http.StatusConflict, "including pages you cannot see"},
		{"a plain member is refused before emptiness is even asked", "mel", "MEMBER", http.StatusForbidden, "MANAGER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-ops", tc.user, tc.role, "")
			expectStatus(t, rr, tc.status, tc.says)
			if strings.Contains(rr.Body.String(), "fleet-201") {
				t.Errorf("the refusal names the page: %s", rr.Body.String())
			}
		})
	}
	// Take the page out (its owner may, alone) and the folder goes.
	rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/pages/fleet-201", "alice", "MEMBER",
		fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
	expectStatus(t, rr, http.StatusOK)
	rr = f.call(t, "DELETE", "/api/v1/page-folders/engine-ops", "mia", "MANAGER", "")
	expectStatus(t, rr, http.StatusNoContent)
	rr = f.call(t, "GET", "/api/v1/page-folders/engine-ops", "erin", "ADMIN", "")
	expectStatus(t, rr, http.StatusNotFound)
	e := f.spy.firstOfType(entryPageFolderChanged)
	var deleted bool
	for _, e := range f.spy.entries {
		if e.Type == entryPageFolderChanged && e.Payload["op"] == "deleted" {
			deleted = true
		}
	}
	if e == nil || !deleted {
		t.Error("the delete was not journalled")
	}
}

// ── A4 move, A5 remove ─────────────────────────────────────────────────────

func TestPageFolders_MoveNeedsBothAuthoritiesInOneCaller(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.createFolder(t, "engine-two", "Engine two", "crew/engine")

	t.Run("the page owner without standing on the folder is refused, and told who can (A4)", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "alice", "MEMBER", f.moveBody(t, "engine-ops", "fleet-201"))
		expectStatus(t, rr, http.StatusForbidden, "MANAGER", "crew/engine", "you may move the page")
	})
	t.Run("the folder's MANAGER who is not the page's owner cannot even see the page", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "mia", "MANAGER", f.moveBody(t, "engine-ops", "fleet-201"))
		expectStatus(t, rr, http.StatusNotFound, "fleet-201")
	})
	t.Run("the folder's MANAGER who sees the page but does not own it is refused as initiator", func(t *testing.T) {
		pagesGrant(t, f.h, f.wsID, f.owner, "fleet-201", `{"subject_type":"user","subject":"mia","level":"read"}`)
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "mia", "MANAGER", f.moveBody(t, "engine-ops", "fleet-201"))
		expectStatus(t, rr, http.StatusForbidden, "initiated by its owner")
	})
	t.Run("an admin moves it (A4)", func(t *testing.T) {
		doc := f.file(t, "engine-ops", "fleet-201")
		page, _ := doc["page"].(map[string]any)
		folder, _ := page["folder"].(map[string]any)
		if folder["slug"] != "engine-ops" || page["pages_version"] != float64(1) {
			t.Errorf("moved row = %v, want folder engine-ops and pages_version 1", page)
		}
		e := f.spy.firstOfType(entryPageFolderMembershipChanged)
		if e == nil || e.Payload["op"] != "moved" || e.Payload["to"] != "engine-ops" || e.Payload["from"] != "" {
			t.Errorf("journal: %+v", e)
		}
	})
	t.Run("filing it again where it already is changes nothing", func(t *testing.T) {
		doc := f.file(t, "engine-ops", "fleet-201")
		page, _ := doc["page"].(map[string]any)
		if page["pages_version"] != float64(1) {
			t.Errorf("pages_version bumped on a no-op move: %v", page["pages_version"])
		}
	})
	t.Run("the page owner who is a MANAGER in the target crew moves it between folders (A4)", func(t *testing.T) {
		pagesJoinCrew(t, f.h, "alice", "crew-engine")
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-two/pages", "alice", "MANAGER", f.moveBody(t, "engine-two", "fleet-201"))
		expectStatus(t, rr, http.StatusOK, `"slug":"engine-two"`)
		if v := f.pagesVersion(t, "fleet-201"); v != 2 {
			t.Errorf("pages_version = %d, want 2 after two moves", v)
		}
		var moved int
		for _, e := range f.spy.entries {
			if e.Type == entryPageFolderMembershipChanged && e.Payload["from"] == "engine-ops" && e.Payload["to"] == "engine-two" {
				moved++
			}
		}
		if moved != 1 {
			t.Errorf("journalled %d folder-to-folder moves, want 1", moved)
		}
	})
	t.Run("removal is the owner's alone, with no folder standing needed (A5)", func(t *testing.T) {
		if _, err := f.h.db.Exec(`DELETE FROM crew_members WHERE user_id = 'alice'`); err != nil {
			t.Fatal(err)
		}
		rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-two/pages/fleet-201", "mia", "MANAGER",
			fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
		expectStatus(t, rr, http.StatusForbidden, "the folder has no say")
		rr = f.call(t, "DELETE", "/api/v1/page-folders/engine-two/pages/fleet-201", "alice", "MEMBER",
			fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
		expectStatus(t, rr, http.StatusOK, `"folder":null`, `"pages_version":3`)
	})
	t.Run("removing from a folder the page is not in is the stale-fence conflict", func(t *testing.T) {
		rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-two/pages/fleet-201", "alice", "MEMBER",
			fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
		expectStatus(t, rr, http.StatusConflict, `"conflict":"pages_version"`, `"pages_version":3`)
	})
}

// ── A11 stale fences ───────────────────────────────────────────────────────

func TestPageFolders_StaleFencesAre409WithTheCurrentPair(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	if _, err := f.h.db.Exec(`UPDATE page_folders SET grants_version = 4 WHERE slug = 'engine-ops'`); err != nil {
		t.Fatal(err)
	}
	f.file(t, "engine-ops", "fleet-201") // pages_version → 1
	cases := []struct {
		name     string
		body     string
		status   int
		conflict string
	}{
		{"a stale pages_version", `{"page":"fleet-201","pages_version":0,"grants_version":4}`, http.StatusConflict, "pages_version"},
		{"a stale grants_version", `{"page":"fleet-201","pages_version":1,"grants_version":3}`, http.StatusConflict, "grants_version"},
		{"both stale names the page's first", `{"page":"fleet-201","pages_version":9,"grants_version":9}`, http.StatusConflict, "pages_version"},
		{"a missing fence is a 400, never a guess", `{"page":"fleet-201","pages_version":1}`, http.StatusBadRequest, ""},
		{"the current pair goes through", `{"page":"fleet-201","pages_version":1,"grants_version":4}`, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "erin", "ADMIN", tc.body)
			expectStatus(t, rr, tc.status)
			if tc.conflict == "" {
				return
			}
			doc := decodeFoldersJSON(t, rr)
			if doc["conflict"] != tc.conflict || doc["pages_version"] != float64(1) || doc["grants_version"] != float64(4) {
				t.Errorf("409 body = %v, want conflict %q with pages_version 1 and grants_version 4", doc, tc.conflict)
			}
		})
	}
	t.Run("a removal with a stale fence", func(t *testing.T) {
		rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/pages/fleet-201", "erin", "ADMIN", `{"pages_version":0}`)
		expectStatus(t, rr, http.StatusConflict, `"conflict":"pages_version"`, `"grants_version":4`)
		rr = f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/pages/fleet-201", "erin", "ADMIN", `{}`)
		expectStatus(t, rr, http.StatusBadRequest, "pages_version is required")
	})
}

// ── A9 filtered read ───────────────────────────────────────────────────────

func TestPageFolders_ReadIsFilteredToWhatTheCallerReaches(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.createFolder(t, "lookout-ops", "Lookout ops", "crew/lookout")
	pagesCreateWith(t, f.h, f.wsID, f.owner, pagesReachBody("ops-board", "lookout"))
	f.file(t, "engine-ops", "fleet-201")
	f.file(t, "engine-ops", "ops-board")

	cases := []struct {
		name   string
		user   string
		role   string
		listed map[string]float64 // slug → page_count; absent = not listed
		show   int                // status of GET engine-ops
		pages  int
	}{
		{"an admin sees both folders and every page", "erin", "ADMIN", map[string]float64{"engine-ops": 2, "lookout-ops": 0}, http.StatusOK, 2},
		{"a grantee of one page sees the folder with that one page and count 1 (A9)", "dave", "MEMBER", map[string]float64{"engine-ops": 1}, http.StatusOK, 1},
		{"the page owner likewise", "alice", "MEMBER", map[string]float64{"engine-ops": 1}, http.StatusOK, 1},
		{"a member of the owning crew sees the folder empty, not the pages", "mel", "MEMBER", map[string]float64{"engine-ops": 0}, http.StatusOK, 0},
		{"no reach and no standing: not listed, and the folder is a 404 (A9)", "frank", "MEMBER", map[string]float64{}, http.StatusNotFound, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "GET", "/api/v1/page-folders", tc.user, tc.role, "")
			expectStatus(t, rr, http.StatusOK)
			var doc struct {
				Folders []map[string]any `json:"folders"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			got := map[string]float64{}
			for _, row := range doc.Folders {
				got[row["slug"].(string)] = row["page_count"].(float64)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.listed) {
				t.Errorf("listed %v, want %v", got, tc.listed)
			}
			rr = f.call(t, "GET", "/api/v1/page-folders/engine-ops", tc.user, tc.role, "")
			expectStatus(t, rr, tc.show)
			if tc.show != http.StatusOK {
				return
			}
			show := decodeFoldersJSON(t, rr)
			pages, _ := show["pages"].([]any)
			if len(pages) != tc.pages || show["page_count"] != float64(tc.pages) {
				t.Errorf("show: %d pages, page_count %v, want %d", len(pages), show["page_count"], tc.pages)
			}
			for _, p := range pages {
				row := p.(map[string]any)
				if reach, _ := row["reach"].([]any); len(reach) == 0 {
					t.Errorf("row %v has no reach", row["slug"])
				}
				if folder, _ := row["folder"].(map[string]any); folder["slug"] != "engine-ops" {
					t.Errorf("row %v carries folder %v", row["slug"], row["folder"])
				}
			}
		})
	}
}

// The page index and the single-page read both carry the folder and the
// fence, so the rail and the move dialog need no second call.
func TestPageFolders_PageRowsCarryTheFolderAndTheFence(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	pagesCreateWith(t, f.h, f.wsID, f.owner, pagesReachBody("unfiled", "lookout"))

	rows := pagesListRows(t, f.h, f.wsID, "erin", "ADMIN")
	if v, present := rows["unfiled"]["folder"]; !present || v != nil {
		t.Errorf("unfiled row folder = %v (present %v), want an explicit null", v, present)
	}
	if rows["unfiled"]["pages_version"] != float64(0) {
		t.Errorf("unfiled pages_version = %v, want 0", rows["unfiled"]["pages_version"])
	}
	f.file(t, "engine-ops", "fleet-201")
	rows = pagesListRows(t, f.h, f.wsID, "erin", "ADMIN")
	folder, _ := rows["fleet-201"]["folder"].(map[string]any)
	if folder["slug"] != "engine-ops" || folder["name"] != "Engine ops" || rows["fleet-201"]["pages_version"] != float64(1) {
		t.Errorf("filed row = folder %v, pages_version %v", folder, rows["fleet-201"]["pages_version"])
	}
	doc := pagesGet(t, f.h, f.wsID, "erin", "ADMIN", "fleet-201")
	folder, _ = doc["folder"].(map[string]any)
	if folder["slug"] != "engine-ops" || doc["pages_version"] != float64(1) {
		t.Errorf("get: folder %v, pages_version %v", doc["folder"], doc["pages_version"])
	}
	doc = pagesGet(t, f.h, f.wsID, "erin", "ADMIN", "unfiled")
	if v, present := doc["folder"]; !present || v != nil {
		t.Errorf("get unfiled: folder = %v (present %v), want an explicit null", v, present)
	}
}
