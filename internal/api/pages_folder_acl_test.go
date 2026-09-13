package api

// Pages folders — inherited permissions (#2533), the acceptance scenarios of
// docs/prd/pages-folder-permissions-linux-model-2026-09-13.md §8: A1 `w`
// implies `r`, A2 what a `w` holder may and may not do, A3 who adds a page,
// A4 an entry outlives its setter, A5 the workspace subject, A6 a sealed
// panel stays sealed, A7 the ACL is a manager's, A8 a stale acl_version,
// A9 the batch move is all or nothing, A10 the revocation boundary, A11 the
// constant statement count (in pages_list_reach_test.go), A12 the workspace
// subject on a page grant. Every rule has its refused case next to its
// allowed one.
//
// The cast is newFoldersFixture's: mia MANAGER in crew/engine, mel MEMBER in
// crew/engine, max MANAGER in no crew, erin ADMIN, alice MEMBER who owns
// fleet-201 (its one panel is crew/lookout's), dave with a read grant on it,
// frank MEMBER who reaches nothing.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// share sets one entry as the admin and returns the ACL document.
func (f *foldersFixture) share(t *testing.T, folder, subjectType, subjectID string, write bool) map[string]any {
	t.Helper()
	rr := f.call(t, "PUT", "/api/v1/page-folders/"+folder+"/acl", "erin", "ADMIN",
		fmt.Sprintf(`{"subject_type":%q,"subject_id":%q,"can_write":%v}`, subjectType, subjectID, write))
	if rr.Code != http.StatusOK {
		t.Fatalf("share %s with %s/%s: %d %s", folder, subjectType, subjectID, rr.Code, rr.Body.String())
	}
	return decodeFoldersJSON(t, rr)
}

func (f *foldersFixture) unshare(t *testing.T, folder, subjectType, subjectID string) {
	t.Helper()
	rr := f.call(t, "DELETE", "/api/v1/page-folders/"+folder+"/acl/"+subjectType+"/"+subjectID, "erin", "ADMIN", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unshare %s/%s from %s: %d %s", subjectType, subjectID, folder, rr.Code, rr.Body.String())
	}
}

// reachOf is the caller's reach on one listed page, or nil when the page is
// not listed for them.
func (f *foldersFixture) reachOf(t *testing.T, user, role, page string) []string {
	t.Helper()
	row := pagesListRows(t, f.h, f.wsID, user, role)[page]
	if row == nil {
		return nil
	}
	return pagesReachOf(t, row)
}

func (f *foldersFixture) folderOf(t *testing.T, page string) string {
	t.Helper()
	var folder string
	if err := f.h.db.QueryRow(`SELECT COALESCE(folder_id, '') FROM pages WHERE workspace_id = ? AND slug = ?`, f.wsID, page).Scan(&folder); err != nil {
		t.Fatalf("folder of %s: %v", page, err)
	}
	return folder
}

// pageCall routes a page request (PATCH page, GET/PUT project) to the handler.
func (f *foldersFixture) pageCall(t *testing.T, method, path, user, role, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, method, path, f.wsID, user, role, body)
	rest := strings.TrimPrefix(path, "/api/v1/pages/")
	req.SetPathValue("slug", strings.Split(rest, "/")[0])
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// ── A1 ─────────────────────────────────────────────────────────────────────

func TestPageFolderACL_WriteImpliesReadAndTheShapeCannotSayOtherwise(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	cases := []struct {
		name   string
		body   string
		status int
		says   string
	}{
		{"can_write without can_read is refused (A1)", `{"subject_type":"user","subject_id":"frank","can_read":false,"can_write":true}`, http.StatusBadRequest, "can_read cannot be false"},
		{"an agent is never a subject", `{"subject_type":"agent","subject_id":"watcher","can_write":false}`, http.StatusBadRequest, "never an agent"},
		{"a subject kind outside the vocabulary", `{"subject_type":"team","subject_id":"x"}`, http.StatusBadRequest, `subject_type must be`},
		{"a user who is not a member", `{"subject_type":"user","subject_id":"nobody@example.com"}`, http.StatusBadRequest, "no member of this workspace"},
		{"a user needs an id or an email", `{"subject_type":"user"}`, http.StatusBadRequest, "subject_id is required"},
		{"can edit stores read too", `{"subject_type":"user","subject_id":"frank","can_write":true}`, http.StatusOK, `"can_read":true`},
		{"the same entry again is the same state", `{"subject_type":"user","subject_id":"frank@example.com","can_write":true}`, http.StatusOK, `"can_write":true`},
		{"a crew by slug, can view", `{"subject_type":"crew","subject_id":"lookout"}`, http.StatusOK, `"label":"lookout"`},
		{"everyone in the workspace, no id", `{"subject_type":"workspace"}`, http.StatusOK, `"subject_id":""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "PUT", "/api/v1/page-folders/engine-ops/acl", "erin", "ADMIN", tc.body)
			expectStatus(t, rr, tc.status, tc.says)
		})
	}
	var rows, reads int
	if err := f.h.db.QueryRow(`SELECT COUNT(*), SUM(can_read) FROM page_folder_acl`).Scan(&rows, &reads); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || reads != 3 {
		t.Errorf("stored %d rows with %d can_read; want 3 rows, every one readable", rows, reads)
	}
	doc := decodeFoldersJSON(t, f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", "mia", "MANAGER", ""))
	if doc["acl_version"] != float64(4) {
		t.Errorf("acl_version = %v, want 4 after four writes", doc["acl_version"])
	}
	acl, _ := doc["acl"].([]any)
	got := map[string]map[string]any{}
	for _, e := range acl {
		entry := e.(map[string]any)
		got[entry["subject_type"].(string)+"/"+entry["subject_id"].(string)] = entry
	}
	if len(got) != 3 || got["user/frank"]["label"] != "frank@example.com" || got["user/frank"]["can_write"] != true ||
		got["crew/crew-lookout"]["can_write"] != false || got["workspace/"]["label"] != "workspace" ||
		got["user/frank"]["set_by"] != "erin@example.com" {
		t.Errorf("acl = %v", got)
	}
	if e := f.spy.firstOfType(entryPageFolderACLChanged); e == nil || e.Payload["op"] != "set" || e.Payload["subject_id"] != "frank" || e.Payload["can_write"] != true {
		t.Errorf("journal: %+v", e)
	}
	// The CHECK is the same rule below the handler.
	if _, err := f.h.db.Exec(`INSERT INTO page_folder_acl (folder_id, subject_type, subject_id, can_read, can_write, set_at)
		SELECT id, 'user', 'mel', 0, 1, 'now' FROM page_folders WHERE slug = 'engine-ops'`); err == nil {
		t.Error("the schema accepted can_read = 0")
	}
	if _, err := f.h.db.Exec(`INSERT INTO page_folder_acl (folder_id, subject_type, subject_id, set_at)
		SELECT id, 'agent', 'watcher', 'now' FROM page_folders WHERE slug = 'engine-ops'`); err == nil {
		t.Error("the schema accepted an agent subject")
	}
}

// ── A2, A6 ─────────────────────────────────────────────────────────────────

func TestPageFolderACL_WriteHolderArrangesAndEditsButNeverMovesDeletesOrShares(t *testing.T) {
	f := newFoldersFixture(t)
	f.h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.createFolder(t, "engine-two", "Engine two", "crew/engine")
	f.file(t, "engine-ops", "fleet-201")
	f.share(t, "engine-ops", "user", "frank", true)
	f.share(t, "engine-ops", "crew", "engine", false)
	f.share(t, "engine-two", "user", "frank", true)
	if rr := projectPut(t, f.h, f.wsID, "erin", "ADMIN", "fleet-201", 0, projectTestSource()); rr.Code != http.StatusOK {
		t.Fatalf("seed project: %d %s", rr.Code, rr.Body.String())
	}

	t.Run("the folder's ACL is a path, in the fixed place, for everyone it names", func(t *testing.T) {
		for user, want := range map[string][]string{
			"frank": {"folder:engine-ops"},
			"mel":   {"folder:engine-ops"},
			"dave":  {"grant"},
			"alice": {"owner"},
		} {
			role := "MEMBER"
			if got := f.reachOf(t, user, role, "fleet-201"); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: reach = %v, want %v", user, got, want)
			}
		}
		if got := f.reachOf(t, "max", "MANAGER", "fleet-201"); got != nil {
			t.Errorf("max reaches fleet-201 by %v; nobody named him", got)
		}
	})
	t.Run("a `w` holder renames the folder (A2)", func(t *testing.T) {
		rr := f.call(t, "PATCH", "/api/v1/page-folders/engine-ops", "frank", "MEMBER", `{"name":"Frank's board","icon":"chart"}`)
		expectStatus(t, rr, http.StatusOK, `"name":"Frank's board"`, `"shared":"crew"`)
		rr = f.call(t, "PATCH", "/api/v1/page-folders/engine-ops", "mel", "MEMBER", `{"name":"Mel's"}`)
		expectStatus(t, rr, http.StatusForbidden, "let edit it")
	})
	t.Run("a `w` holder edits a page inside; `r` alone does not (A2)", func(t *testing.T) {
		rr := f.pageCall(t, "PATCH", "/api/v1/pages/fleet-201", "frank", "MEMBER", `{"name":"Renamed by frank"}`, f.h.Update)
		expectStatus(t, rr, http.StatusOK, `"name":"Renamed by frank"`)
		rr = f.pageCall(t, "PATCH", "/api/v1/pages/fleet-201", "mel", "MEMBER", `{"name":"Renamed by mel"}`, f.h.Update)
		expectStatus(t, rr, http.StatusForbidden)
	})
	t.Run("a sealed panel stays sealed through the folder, and the whole document is withheld (A2, A6)", func(t *testing.T) {
		for _, user := range []string{"frank", "mel"} {
			doc := pagesGet(t, f.h, f.wsID, user, "MEMBER", "fleet-201")
			pagesAssertSealedPlaceholder(t, pagesPanel(t, doc, "p0"), "p0")
			rr := f.pageCall(t, "GET", "/api/v1/pages/fleet-201/project", user, "MEMBER", "", f.h.GetProject)
			expectStatus(t, rr, http.StatusForbidden)
		}
		// `w` reaches the project surface (mayEditSpec) and is then refused
		// by #2502's whole-document check, in that order.
		rr := f.pageCall(t, "GET", "/api/v1/pages/fleet-201/project", "frank", "MEMBER", "", f.h.GetProject)
		expectStatus(t, rr, http.StatusForbidden, "every panel")
		rr = projectPut(t, f.h, f.wsID, "frank", "MEMBER", "fleet-201", 1, projectTestSource())
		expectStatus(t, rr, http.StatusForbidden, "every panel")
		rr = f.pageCall(t, "GET", "/api/v1/pages/fleet-201/project", "mel", "MEMBER", "", f.h.GetProject)
		expectStatus(t, rr, http.StatusForbidden, "edit permission")
	})
	t.Run("a `w` holder cannot move the page elsewhere, delete the folder or change the ACL (A2)", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-two/pages", "frank", "MEMBER", f.moveBody(t, "engine-two", "fleet-201"))
		expectStatus(t, rr, http.StatusForbidden, "initiated by its owner")
		rr = f.call(t, "DELETE", "/api/v1/page-folders/engine-two", "frank", "MEMBER", "")
		expectStatus(t, rr, http.StatusForbidden, "deleting folder")
		rr = f.call(t, "PUT", "/api/v1/page-folders/engine-ops/acl", "frank", "MEMBER", `{"subject_type":"user","subject_id":"mel","can_write":true}`)
		expectStatus(t, rr, http.StatusForbidden, "MANAGER")
		rr = f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/acl/crew/engine", "frank", "MEMBER", "")
		expectStatus(t, rr, http.StatusForbidden, "MANAGER")
		rr = f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", "frank", "MEMBER", "")
		expectStatus(t, rr, http.StatusForbidden, "sharing label")
	})
	t.Run("a `w` holder removes a foreign page, and everyone else loses the inherited path (A2)", func(t *testing.T) {
		rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/pages/fleet-201", "frank", "MEMBER",
			fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
		expectStatus(t, rr, http.StatusOK, `"folder":null`, `"reach":[]`)
		if got := f.reachOf(t, "mel", "MEMBER", "fleet-201"); got != nil {
			t.Errorf("mel still reaches fleet-201 by %v after the removal", got)
		}
		if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); got != nil {
			t.Errorf("frank still reaches fleet-201 by %v after the removal", got)
		}
		if got := f.reachOf(t, "dave", "MEMBER", "fleet-201"); !reflect.DeepEqual(got, []string{"grant"}) {
			t.Errorf("dave's own grant should survive: reach = %v", got)
		}
		e := f.spy.entries[len(f.spy.entries)-1]
		if e.Type != entryPageFolderMembershipChanged || e.Payload["op"] != "removed" || e.ActorID != "frank" {
			t.Errorf("journal: %+v", e)
		}
	})
}

// ── A3 ─────────────────────────────────────────────────────────────────────

func TestPageFolderACL_AddNeedsOwnershipAndStandingOnTheTarget(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.share(t, "engine-ops", "user", "frank", true)
	pagesGrant(t, f.h, f.wsID, f.owner, "fleet-201", `{"subject_type":"user","subject":"frank","level":"read"}`)
	cases := []struct {
		name   string
		user   string
		role   string
		status int
		says   string
	}{
		{"`w` on the target without owning the page is refused (A3)", "frank", "MEMBER", http.StatusForbidden, "initiated by its owner"},
		{"the owner without standing on the target is refused, with the sentence (A3)", "alice", "MEMBER", http.StatusForbidden, "you may move the page, but not into this folder"},
		{"an admin moves it (A3)", "erin", "ADMIN", http.StatusOK, `"slug":"engine-ops"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", tc.user, tc.role, f.moveBody(t, "engine-ops", "fleet-201"))
			expectStatus(t, rr, tc.status, tc.says)
		})
	}
	t.Run("the owner with `w` on the target moves it, and the sentence names the `w` holder as an acceptor", func(t *testing.T) {
		f.createFolder(t, "engine-two", "Engine two", "crew/engine")
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-two/pages", "alice", "MEMBER", f.moveBody(t, "engine-two", "fleet-201"))
		expectStatus(t, rr, http.StatusForbidden, "its permissions let edit it")
		f.share(t, "engine-two", "user", "alice", true)
		rr = f.call(t, "POST", "/api/v1/page-folders/engine-two/pages", "alice", "MEMBER", f.moveBody(t, "engine-two", "fleet-201"))
		expectStatus(t, rr, http.StatusOK, `"slug":"engine-two"`)
	})
}

// ── A4 ─────────────────────────────────────────────────────────────────────

func TestPageFolderACL_EntriesOutliveTheirSetter(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.file(t, "engine-ops", "fleet-201")
	rr := f.call(t, "PUT", "/api/v1/page-folders/engine-ops/acl", "mia", "MANAGER", `{"subject_type":"user","subject_id":"frank","can_write":false}`)
	expectStatus(t, rr, http.StatusOK, `"set_by":"mia@example.com"`)
	rr = f.call(t, "PUT", "/api/v1/page-folders/engine-ops/acl", "mia", "MANAGER", `{"subject_type":"user","subject_id":"dave","can_write":false}`)
	expectStatus(t, rr, http.StatusOK)

	// mia leaves the crew and the workspace: the entry she set still names
	// frank (A4) — unlike a page grant, whose issuer is re-checked at use.
	for _, q := range []string{
		`DELETE FROM crew_members WHERE user_id = 'mia'`,
		`DELETE FROM workspace_members WHERE user_id = 'mia'`,
	} {
		if _, err := f.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); !reflect.DeepEqual(got, []string{"folder:engine-ops"}) {
		t.Errorf("after mia left: frank's reach = %v, want [folder:engine-ops]", got)
	}
	// mia's account is deleted: the entries stay, set_by is forgotten.
	if _, err := f.h.db.Exec(`DELETE FROM users WHERE id = 'mia'`); err != nil {
		t.Fatalf("delete mia: %v", err)
	}
	doc := decodeFoldersJSON(t, f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", "erin", "ADMIN", ""))
	acl, _ := doc["acl"].([]any)
	if len(acl) != 2 {
		t.Fatalf("acl after the setter was deleted: %v", acl)
	}
	for _, e := range acl {
		entry := e.(map[string]any)
		if entry["set_by"] != "" || entry["set_by_user_id"] != "" {
			t.Errorf("entry %v still names a setter", entry)
		}
	}
	if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); !reflect.DeepEqual(got, []string{"folder:engine-ops"}) {
		t.Errorf("after mia was deleted: frank's reach = %v", got)
	}
	// A deleted SUBJECT's own entry goes (§3/13); the other stays.
	if _, err := f.h.db.Exec(`DELETE FROM users WHERE id = 'dave'`); err != nil {
		t.Fatalf("delete dave: %v", err)
	}
	doc = decodeFoldersJSON(t, f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", "erin", "ADMIN", ""))
	acl, _ = doc["acl"].([]any)
	if len(acl) != 1 || acl[0].(map[string]any)["subject_id"] != "frank" {
		t.Errorf("acl after the subject dave was deleted: %v", acl)
	}
}

// ── A5 ─────────────────────────────────────────────────────────────────────

func TestPageFolderACL_WorkspaceSubjectReachesEveryMemberAndNobodyElse(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.file(t, "engine-ops", "fleet-201")
	pagesSeedUser(t, f.h, f.wsID, "zed", "zed@example.com", "MEMBER")
	if _, err := f.h.db.Exec(`DELETE FROM workspace_members WHERE user_id = 'zed'`); err != nil {
		t.Fatal(err)
	}
	pagesSeedAgent(t, f.h, f.wsID, "agent-watcher", "watcher", "crew-engine")

	if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); got != nil {
		t.Fatalf("frank reaches fleet-201 by %v before anything was shared", got)
	}
	f.share(t, "engine-ops", "workspace", "", false)

	for _, u := range []struct{ id, role string }{{"frank", "MEMBER"}, {"max", "MANAGER"}, {"mel", "MEMBER"}} {
		if got := f.reachOf(t, u.id, u.role, "fleet-201"); !reflect.DeepEqual(got, []string{"folder:engine-ops"}) {
			t.Errorf("%s: reach = %v, want [folder:engine-ops] (A5)", u.id, got)
		}
		doc := pagesGet(t, f.h, f.wsID, u.id, u.role, "fleet-201")
		pagesAssertSealedPlaceholder(t, pagesPanel(t, doc, "p0"), "p0")
	}
	t.Run("a member sees the folder as shared with the workspace, without names", func(t *testing.T) {
		rr := f.call(t, "GET", "/api/v1/page-folders", "frank", "MEMBER", "")
		expectStatus(t, rr, http.StatusOK, `"shared":"workspace"`, `"page_count":1`)
		if body := rr.Body.String(); strings.Contains(body, `"acl"`) || strings.Contains(body, "example.com") {
			t.Errorf("the listing carries the ACL or a name: %s", body)
		}
		rr = f.call(t, "GET", "/api/v1/page-folders/engine-ops", "frank", "MEMBER", "")
		expectStatus(t, rr, http.StatusOK, `"shared":"workspace"`)
	})
	t.Run("a user from another workspace gets the 404 (A5)", func(t *testing.T) {
		req := pagesRequest(t, "GET", "/api/v1/pages/fleet-201", f.wsID, "zed", "", "")
		req.SetPathValue("slug", "fleet-201")
		rr := httptest.NewRecorder()
		f.h.Get(rr, req)
		expectStatus(t, rr, http.StatusNotFound)
		if rows := pagesListRows(t, f.h, f.wsID, "zed", ""); rows["fleet-201"] != nil {
			t.Errorf("zed is listed fleet-201: %v", rows["fleet-201"])
		}
	})
	t.Run("an agent never (A5)", func(t *testing.T) {
		rec, err := f.h.loadPage(t.Context(), f.wsID, "fleet-201")
		if err != nil {
			t.Fatal(err)
		}
		panels, err := f.h.loadPanels(t.Context(), f.wsID, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		viewer, err := f.h.agentViewer(t.Context(), f.wsID, "agent-watcher")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := f.h.canSeePage(t.Context(), f.wsID, rec, panels, viewer); err != nil || ok {
			t.Errorf("the agent reaches the page through the workspace entry: %v %v", ok, err)
		}
	})
	t.Run("removing the entry takes the path away, and its path spells the type twice", func(t *testing.T) {
		f.unshare(t, "engine-ops", "workspace", "workspace")
		if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); got != nil {
			t.Errorf("frank still reaches fleet-201 by %v", got)
		}
		rr := f.call(t, "DELETE", "/api/v1/page-folders/engine-ops/acl/workspace/workspace", "erin", "ADMIN", "")
		expectStatus(t, rr, http.StatusNotFound, "no permission entry")
		rr = f.call(t, "GET", "/api/v1/page-folders", "erin", "ADMIN", "")
		expectStatus(t, rr, http.StatusOK, `"shared":"none"`)
	})
}

// ── A7, A8 ─────────────────────────────────────────────────────────────────

func TestPageFolderACL_OnlyManagersReadTheACLEvenInA409(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.share(t, "engine-ops", "user", "alice", true)
	f.share(t, "engine-ops", "user", "dave", false)
	cases := []struct {
		name   string
		user   string
		role   string
		status int
	}{
		{"the owning crew's MANAGER reads it", "mia", "MANAGER", http.StatusOK},
		{"an admin reads it", "erin", "ADMIN", http.StatusOK},
		{"a member of the crew is refused (A7)", "mel", "MEMBER", http.StatusForbidden},
		{"a `w` holder is refused (A7)", "alice", "MEMBER", http.StatusForbidden},
		{"an `r` holder is refused (A7)", "dave", "MEMBER", http.StatusForbidden},
		{"a MANAGER who cannot see the folder gets the 404", "max", "MANAGER", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", tc.user, tc.role, "")
			expectStatus(t, rr, tc.status)
			if tc.status != http.StatusOK && strings.Contains(rr.Body.String(), "dave") {
				t.Errorf("the refusal names a subject: %s", rr.Body.String())
			}
		})
	}
	t.Run("a stale acl_version is a 409: the reader gets the versions and a sentence, the manager the ACL (A7, A8)", func(t *testing.T) {
		stale := fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d}`, f.pagesVersion(t, "fleet-201"), f.aclVersion(t, "engine-ops")-1)
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "alice", "MEMBER", stale)
		expectStatus(t, rr, http.StatusConflict, `"conflict":"acl_version"`, "look at the preview again")
		doc := decodeFoldersJSON(t, rr)
		if _, present := doc["acl"]; present {
			t.Errorf("the owner's 409 carries the ACL: %v", doc)
		}
		if doc["acl_version"] != float64(2) || doc["pages_version"] != float64(0) {
			t.Errorf("409 versions = %v", doc)
		}
		if strings.Contains(rr.Body.String(), "dave") {
			t.Errorf("the owner's 409 names a subject: %s", rr.Body.String())
		}
		pagesGrant(t, f.h, f.wsID, f.owner, "fleet-201", `{"subject_type":"user","subject":"erin","level":"read"}`)
		rr = f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "erin", "ADMIN", stale)
		expectStatus(t, rr, http.StatusConflict, `"conflict":"acl_version"`, `"subject_id":"dave"`)
		doc = decodeFoldersJSON(t, rr)
		if acl, _ := doc["acl"].([]any); len(acl) != 2 {
			t.Errorf("the manager's 409 carries %d entries, want 2: %v", len(acl), doc)
		}
		if f.folderOf(t, "fleet-201") != "" {
			t.Error("a refused move filed the page")
		}
	})
}

// ── A9 ─────────────────────────────────────────────────────────────────────

func TestPageFolderACL_BatchMoveIsAllOrNothingAndNamesThePage(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	second := pagesCreateWith(t, f.h, f.wsID, f.owner, pagesReachBody("fleet-202", "lookout"))
	pagesSetOwner(t, f.h, second, "alice", "")
	pagesCreateWith(t, f.h, f.wsID, f.owner, pagesReachBody("not-mine", "lookout"))
	pagesGrant(t, f.h, f.wsID, f.owner, "not-mine", `{"subject_type":"user","subject":"alice","level":"read"}`)
	f.share(t, "engine-ops", "user", "alice", true)
	body := func(items ...string) string {
		return fmt.Sprintf(`{"pages":[%s],"acl_version":%d}`, strings.Join(items, ","), f.aclVersion(t, "engine-ops"))
	}
	item := func(page string, version int64) string {
		return fmt.Sprintf(`{"page":%q,"pages_version":%d}`, page, version)
	}
	assertNothingMoved := func(t *testing.T) {
		t.Helper()
		for _, page := range []string{"fleet-201", "fleet-202", "not-mine"} {
			if f.folderOf(t, page) != "" {
				t.Errorf("%s was filed by a refused batch", page)
			}
		}
	}
	t.Run("one page the caller does not own refuses the whole batch and is named (A9)", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER",
			body(item("fleet-201", 0), item("not-mine", 0)))
		expectStatus(t, rr, http.StatusForbidden, `not-mine`, "nothing was moved")
		assertNothingMoved(t)
	})
	t.Run("one stale pages_version refuses the whole batch and is named (A9)", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER",
			body(item("fleet-201", 0), item("fleet-202", 7)))
		expectStatus(t, rr, http.StatusConflict, `"page":"fleet-202"`, `"conflict":"pages_version"`)
		assertNothingMoved(t)
	})
	t.Run("a stale acl_version refuses before any page is looked at", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER",
			`{"pages":[{"page":"fleet-201","pages_version":0}],"acl_version":99}`)
		expectStatus(t, rr, http.StatusConflict, `"conflict":"acl_version"`)
		if strings.Contains(rr.Body.String(), `"acl"`) {
			t.Errorf("the owner's 409 carries the ACL: %s", rr.Body.String())
		}
		assertNothingMoved(t)
	})
	t.Run("a missing fence or an empty list is a 400", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER", `{"pages":[{"page":"fleet-201"}],"acl_version":1}`)
		expectStatus(t, rr, http.StatusBadRequest, "pages_version")
		rr = f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER", `{"pages":[],"acl_version":1}`)
		expectStatus(t, rr, http.StatusBadRequest, "pages is required")
	})
	t.Run("every page checks out: all move in one go", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages:batch", "alice", "MEMBER",
			body(item("fleet-201", 0), item("fleet-202", 0)))
		expectStatus(t, rr, http.StatusOK, `"acl_version":1`)
		doc := decodeFoldersJSON(t, rr)
		rows, _ := doc["pages"].([]any)
		if len(rows) != 2 {
			t.Fatalf("moved rows: %v", doc)
		}
		for _, page := range []string{"fleet-201", "fleet-202"} {
			if f.folderOf(t, page) == "" || f.pagesVersion(t, page) != 1 {
				t.Errorf("%s: folder %q, pages_version %d", page, f.folderOf(t, page), f.pagesVersion(t, page))
			}
		}
		var moved int
		for _, e := range f.spy.entries {
			if e.Type == entryPageFolderMembershipChanged && e.Payload["op"] == "moved" {
				moved++
			}
		}
		if moved != 2 {
			t.Errorf("journalled %d moves, want 2", moved)
		}
	})
}

// ── A10 ────────────────────────────────────────────────────────────────────

func TestPageFolderACL_RevocationBoundary(t *testing.T) {
	t.Run("a write that began with `w` completes; the next request is refused (A10)", func(t *testing.T) {
		f := newFoldersFixture(t)
		f.h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
		f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
		f.file(t, "engine-ops", "fleet-201")
		f.share(t, "engine-ops", "user", "frank", true)
		// frank sees every panel, so #2502 is not what refuses him.
		pagesJoinCrew(t, f.h, "frank", "crew-lookout")
		if rr := projectPut(t, f.h, f.wsID, "erin", "ADMIN", "fleet-201", 0, projectTestSource()); rr.Code != http.StatusOK {
			t.Fatalf("seed project: %d %s", rr.Code, rr.Body.String())
		}
		production := f.h.db
		hook := &reviewQueryHook{match: "SELECT revision, spec_json, git_commit FROM page_project_drafts", nth: 1, run: func() {
			f.unshare(t, "engine-ops", "user", "frank")
		}}
		f.h.db = reviewHookedDB(t, reviewDBPath(t, production), hook)
		defer func() { f.h.db = production }()

		rr := projectPut(t, f.h, f.wsID, "frank", "MEMBER", "fleet-201", 1, projectTestSource())
		if !hook.didFire() {
			t.Fatal("the interleaved removal did not run")
		}
		expectStatus(t, rr, http.StatusOK)
		rr = projectPut(t, f.h, f.wsID, "frank", "MEMBER", "fleet-201", 2, projectTestSource())
		expectStatus(t, rr, http.StatusForbidden)
		if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); !reflect.DeepEqual(got, []string{"panel_crew:lookout"}) {
			t.Errorf("frank's reach = %v, want the crew path alone now that the folder path is gone", got)
		}
	})
	t.Run("a move and an ACL change that interleave: the move gets the 409 (A10)", func(t *testing.T) {
		f := newFoldersFixture(t)
		f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
		f.share(t, "engine-ops", "user", "alice", true)
		production := f.h.db
		hook := &reviewQueryHook{match: "UPDATE pages SET folder_id", nth: 1, run: func() {
			f.share(t, "engine-ops", "user", "frank", false)
		}}
		f.h.db = reviewHookedDB(t, reviewDBPath(t, production), hook)
		defer func() { f.h.db = production }()

		rr := f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "alice", "MEMBER",
			fmt.Sprintf(`{"page":"fleet-201","pages_version":0,"acl_version":%d}`, 1))
		if !hook.didFire() {
			t.Fatal("the interleaved ACL change did not run")
		}
		expectStatus(t, rr, http.StatusConflict, `"conflict":"acl_version"`, `"acl_version":2`)
		if strings.Contains(rr.Body.String(), `"acl"`) {
			t.Errorf("the owner's 409 carries the ACL: %s", rr.Body.String())
		}
		if f.folderOf(t, "fleet-201") != "" || f.pagesVersion(t, "fleet-201") != 0 {
			t.Error("the page moved under permissions the caller never saw")
		}
	})
}

// ── access/me ──────────────────────────────────────────────────────────────

func TestPageAccessMe_IsTheCallersOwnPathsAndNothingElse(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	f.file(t, "engine-ops", "fleet-201")
	f.share(t, "engine-ops", "user", "frank", false)
	call := func(user, role string) *httptest.ResponseRecorder {
		return f.pageCall(t, "GET", "/api/v1/pages/fleet-201/access/me", user, role, "", f.h.MyPageAccess)
	}
	cases := []struct {
		name   string
		user   string
		role   string
		status int
		paths  []string
	}{
		{"an `r` holder sees the folder path", "frank", "MEMBER", http.StatusOK, []string{"folder:engine-ops"}},
		{"the owner sees ownership", "alice", "MEMBER", http.StatusOK, []string{"owner"}},
		{"a grantee sees the grant", "dave", "MEMBER", http.StatusOK, []string{"grant"}},
		{"an admin sees the role", "erin", "ADMIN", http.StatusOK, []string{"role"}},
		{"no path is the 404", "mel", "MEMBER", http.StatusNotFound, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := call(tc.user, tc.role)
			expectStatus(t, rr, tc.status)
			if tc.status != http.StatusOK {
				return
			}
			doc := decodeFoldersJSON(t, rr)
			var got []string
			for _, p := range doc["paths"].([]any) {
				got = append(got, p.(string))
			}
			if !reflect.DeepEqual(got, tc.paths) || doc["subject_id"] != tc.user || doc["label"] != tc.user+"@example.com" ||
				doc["folder"] != "engine-ops" || doc["shared"] != "crew" {
				t.Errorf("access/me = %v, want paths %v for %s", doc, tc.paths, tc.user)
			}
			for key := range doc {
				if key == "acl" || key == "subjects" {
					t.Errorf("access/me carries %q", key)
				}
			}
		})
	}
}

// ── A12 ────────────────────────────────────────────────────────────────────

func TestPageGrants_WorkspaceSubjectReadsAndWritesNeverProduces(t *testing.T) {
	f := newFoldersFixture(t)
	if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); got != nil {
		t.Fatalf("frank reaches fleet-201 by %v before any grant", got)
	}
	cases := []struct {
		name   string
		body   string
		status int
		says   string
	}{
		{"read to everyone (A12)", `{"subject_type":"workspace","level":"read"}`, http.StatusOK, `"subject":"workspace"`},
		{"write to everyone (A12)", `{"subject_type":"workspace","level":"write"}`, http.StatusOK, `"level":"write"`},
		{"produce to everyone is refused (A12)", `{"subject_type":"workspace","level":"produce"}`, http.StatusBadRequest, "everyone is not one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := pagesGrantCall(t, f.h, "PUT", "/api/v1/pages/fleet-201/grants", f.wsID, "alice", "MEMBER", "fleet-201", tc.body)
			expectStatus(t, rr, tc.status, tc.says)
		})
	}
	if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); !reflect.DeepEqual(got, []string{"grant"}) {
		t.Errorf("frank's reach = %v, want [grant]", got)
	}
	rr := f.pageCall(t, "PATCH", "/api/v1/pages/fleet-201", "frank", "MEMBER", `{"name":"Everyone edits"}`, f.h.Update)
	expectStatus(t, rr, http.StatusOK)
	if _, err := f.h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
		SELECT id, 'workspace', '', 'produce', 'alice' FROM pages WHERE slug = 'fleet-201'`); err == nil {
		t.Error("the schema accepted produce for the workspace subject")
	}
	t.Run("an agent never borrows the workspace grant", func(t *testing.T) {
		pagesSeedAgent(t, f.h, f.wsID, "agent-watcher", "watcher", "crew-engine")
		rec, err := f.h.loadPage(t.Context(), f.wsID, "fleet-201")
		if err != nil {
			t.Fatal(err)
		}
		if f.h.agentMayEditSpec(t.Context(), f.wsID, rec, "agent-watcher") {
			t.Error("the agent holds write through the workspace grant")
		}
	})
	t.Run("revoked by type alone, both levels at once", func(t *testing.T) {
		rr := pagesGrantCall(t, f.h, "DELETE", "/api/v1/pages/fleet-201/grants?subject_type=workspace", f.wsID, "alice", "MEMBER", "fleet-201", "")
		expectStatus(t, rr, http.StatusOK, `"changed":2`)
		if got := f.reachOf(t, "frank", "MEMBER", "fleet-201"); got != nil {
			t.Errorf("frank still reaches fleet-201 by %v", got)
		}
	})
}
