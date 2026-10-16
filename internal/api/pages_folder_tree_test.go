package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/backup"
)

func treeVersion(t *testing.T, f *foldersFixture) int64 {
	t.Helper()
	var n int64
	if err := f.h.db.QueryRow(`SELECT COALESCE((SELECT version FROM page_folder_structure WHERE workspace_id=?),0)`, f.wsID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func treeCreate(t *testing.T, f *foldersFixture, slug, owner, parent string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"name": slug, "slug": slug, "owner": "crew/" + owner}
	if parent != "" {
		body["parent"] = parent
		body["tree_version"] = treeVersion(t, f)
		var acl int64
		_ = f.h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE workspace_id=? AND slug=?`, f.wsID, parent).Scan(&acl)
		body["acl_version"] = acl
	}
	encoded, _ := json.Marshal(body)
	return f.call(t, "POST", "/api/v1/page-folders", f.owner, "OWNER", string(encoded))
}
func treeMove(t *testing.T, f *foldersFixture, source, parent, user string, version int64) *httptest.ResponseRecorder {
	t.Helper()
	var parentValue any
	if parent != "" {
		parentValue = parent
	}
	body := map[string]any{"parent": parentValue, "tree_version": version}
	if parent != "" {
		var acl int64
		_ = f.h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE workspace_id=? AND slug=?`, f.wsID, parent).Scan(&acl)
		body["acl_version"] = acl
	}
	encoded, _ := json.Marshal(body)
	r := f.request(t, "POST", "/api/v1/page-folders/"+source+"/parent", user, "OWNER", string(encoded))
	r.SetPathValue("slug", source)
	w := httptest.NewRecorder()
	f.h.ReparentFolder(w, r)
	return w
}
func folderID(t *testing.T, f *foldersFixture, slug string) string {
	t.Helper()
	var id string
	if err := f.h.db.QueryRow(`SELECT id FROM page_folders WHERE workspace_id=? AND slug=?`, f.wsID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestFolderTreeMovesPreservePageIdentityAndLeafACL(t *testing.T) {
	f := newFoldersFixture(t)
	for _, slug := range []string{"alpha", "beta"} {
		artifactExpect(t, treeCreate(t, f, slug, "engine", ""), 201)
	}
	artifactExpect(t, treeCreate(t, f, "child", "engine", "alpha"), 201)
	child := folderID(t, f, "child")
	f.file(t, "child", "fleet-201")
	var beforeID, beforeSlug, beforeFolder string
	var beforeVersion int64
	if err := f.h.db.QueryRow(`SELECT id,slug,folder_id,pages_version FROM pages WHERE workspace_id=? AND slug='fleet-201'`, f.wsID).Scan(&beforeID, &beforeSlug, &beforeFolder, &beforeVersion); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES(?,'workspace','',1,0,'2026-01-01')`, folderID(t, f, "beta"))
	hub := startedTestHub(t)
	f.h.hub = hub
	channel := "workspace:" + f.wsID
	observer := hub.AddObserver(channel, "frank", 4)
	defer hub.RemoveObserver(channel, observer)
	artifactExpect(t, treeMove(t, f, "child", "beta", f.owner, treeVersion(t, f)), 200)
	select {
	case raw := <-observer.Frames():
		var frame struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != pageFolderEvent || len(frame.Payload) != 1 || frame.Payload["tree_changed"] != true {
			t.Fatalf("private folder workspace frame=%s err=%v", raw, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("folder mutation did not reach workspace subscriber")
	}
	var id, slug, folder string
	var version int64
	_ = f.h.db.QueryRow(`SELECT id,slug,folder_id,pages_version FROM pages WHERE id=?`, beforeID).Scan(&id, &slug, &folder, &version)
	if id != beforeID || slug != beforeSlug || folder != child || folder != beforeFolder || version != beforeVersion {
		t.Fatal("folder reparent changed Page identity or membership fence")
	}
	if w := pagesGetStatus(t, f.h, f.wsID, "frank", "MEMBER", "fleet-201"); w.Code != 404 {
		t.Fatalf("ancestor ACL inherited: %d %s", w.Code, w.Body.String())
	}
	artifactExpect(t, pagesGetStatus(t, f.h, f.wsID, "dave", "MEMBER", "fleet-201"), 200)
	// Current mutation emits no private organization metadata to workspace clients.
	invalidation, _ := json.Marshal(folderTreeInvalidation())
	if string(invalidation) != `{"tree_changed":true}` {
		t.Fatalf("private invalidation=%s", invalidation)
	}
	entry := f.spy.firstOfType(entryPageFolderTreeChanged)
	if entry == nil {
		t.Fatal("tree journal entry missing")
	}
	encoded, _ := json.Marshal(entry)
	for _, secret := range []string{"child", "beta", child, folderID(t, f, "beta")} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("journal leaked folder identity: %s", encoded)
		}
	}
}
func TestFolderTreeHiddenParentIsMaskedInRawResponses(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "secret-ancestor", "engine", ""), 201)
	artifactExpect(t, treeCreate(t, f, "visible-child", "engine", "secret-ancestor"), 201)
	parent := folderID(t, f, "secret-ancestor")
	child := folderID(t, f, "visible-child")
	execOrFatal(t, f.h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES(?,'user','frank',1,0,'2026-01-01')`, child)
	for _, path := range []string{"/api/v1/page-folders", "/api/v1/page-folders/visible-child"} {
		w := f.call(t, "GET", path, "frank", "MEMBER", "")
		artifactExpect(t, w, 200)
		raw := w.Body.String()
		if strings.Contains(raw, parent) || strings.Contains(raw, "secret-ancestor") {
			t.Fatalf("hidden ancestor leaked: %s", raw)
		}
		if !strings.Contains(raw, `"parent_id":null`) {
			t.Fatalf("visible child was not viewer-rooted: %s", raw)
		}
	}
	execOrFatal(t, f.h.db, `DELETE FROM page_folder_acl WHERE folder_id=?`, child)
	w := f.call(t, "GET", "/api/v1/page-folders", "frank", "MEMBER", "")
	artifactExpect(t, w, 200)
	if strings.Contains(w.Body.String(), "visible-child") {
		t.Fatal("revoked child remains visible")
	}
	artifactExpect(t, f.call(t, "DELETE", "/api/v1/page-folders/secret-ancestor", f.owner, "OWNER", ""), 409)
}
func TestFolderTreeCyclesAndConcurrentReparents(t *testing.T) {
	f := newFoldersFixture(t)
	for _, s := range []string{"a", "b"} {
		artifactExpect(t, treeCreate(t, f, s, "engine", ""), 201)
	}
	v := treeVersion(t, f)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
		wg.Add(1)
		go func(p [2]string) { defer wg.Done(); results <- treeMove(t, f, p[0], p[1], f.owner, v).Code }(pair)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("opposite moves=%v", counts)
	}
	artifactExpect(t, treeMove(t, f, "a", "a", f.owner, treeVersion(t, f)), 409)
	var child, parent string
	if err := f.h.db.QueryRow(`SELECT slug,parent_id FROM page_folders WHERE parent_id IS NOT NULL`).Scan(&child, &parent); err != nil {
		t.Fatal(err)
	}
	var parentSlug string
	_ = f.h.db.QueryRow(`SELECT slug FROM page_folders WHERE id=?`, parent).Scan(&parentSlug)
	artifactExpect(t, treeMove(t, f, parentSlug, child, f.owner, treeVersion(t, f)), 409)
	if _, err := f.h.db.Exec(`UPDATE page_folders SET parent_id=id WHERE id=?`, folderID(t, f, "a")); err == nil {
		t.Fatal("raw SQL self-cycle accepted")
	}
	artifactExpect(t, treeMove(t, f, "a", "", f.owner, v), 409)
}
func TestFolderTreeDepthIncludesHiddenMovedSubtree(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "source", "engine", ""), 201)
	parent := "source"
	for _, slug := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		artifactExpect(t, treeCreate(t, f, slug, "lookout", parent), 201)
		parent = slug
	}
	artifactExpect(t, treeCreate(t, f, "too-deep", "lookout", parent), 409)
	artifactExpect(t, treeCreate(t, f, "target", "engine", ""), 201)
	artifactExpect(t, treeMove(t, f, "source", "target", "mia", treeVersion(t, f)), 409)
	// Mia manages source but cannot see the Lookout-owned descendants.
	artifactExpect(t, f.call(t, "GET", "/api/v1/page-folders/seven", "mia", "MANAGER", ""), 404)
}
func TestFolderTreeDeferredRestoreOrderAndCrossWorkspace(t *testing.T) {
	f := newFoldersFixture(t)
	insert := `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,parent_id) VALUES(?,?,?,?,'crew-engine','2026-01-01','2026-01-01',?)`
	tx, err := f.h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(insert, "restore-child", f.wsID, "restore-child", "Child", "restore-parent"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(insert, "restore-parent", f.wsID, "restore-parent", "Parent", nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = f.h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(insert, "cycle-first", f.wsID, "cycle-first", "A", "cycle-last"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(insert, "cycle-last", f.wsID, "cycle-last", "B", "cycle-first"); err == nil {
		t.Fatal("deferred cyclic insert accepted")
	}
	_ = tx.Rollback()
	execOrFatal(t, f.h.db, `INSERT INTO workspaces(id,name,slug) VALUES('folder-foreign-ws','Foreign','folder-foreign')`)
	if _, err = f.h.db.Exec(`INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,parent_id) VALUES('foreign-child','folder-foreign-ws','foreign','Foreign','crew-engine','2026-01-01','2026-01-01','restore-parent')`); err == nil {
		t.Fatal("crossworkspace parent accepted")
	}
	artifactExpect(t, treeMove(t, f, "restore-child", "missing", f.owner, treeVersion(t, f)), 404)
}
func TestFolderTreeBackupForkPreservesTopology(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "root", "engine", ""), 201)
	artifactExpect(t, treeCreate(t, f, "nested", "engine", "root"), 201)
	f.file(t, "nested", "fleet-201")
	rootID := folderID(t, f, "root")
	childID := folderID(t, f, "nested")
	const pass = "folder-tree-backup-test-password"
	made, err := backup.CreateBackup(t.Context(), f.h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: f.wsID, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: f.owner, Role: "ADMIN"}})
	if err != nil {
		t.Fatal(err)
	}
	db := setupTestDB(t)
	restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: "nested-folder-fork", Actor: backup.Actor{UserID: f.owner, Role: "ADMIN"}})
	if err != nil {
		t.Fatal(err)
	}
	var parent, id, workspace string
	if err = db.QueryRow(`SELECT c.id,c.parent_id,c.workspace_id FROM page_folders c WHERE c.workspace_id=? AND c.slug='nested'`, restored.RestoredWorkspaceID).Scan(&id, &parent, &workspace); err != nil {
		t.Fatal(err)
	}
	if id == childID || parent == rootID || workspace == f.wsID {
		t.Fatal("folder topology IDs not remapped")
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM page_folders WHERE id=? AND workspace_id=? AND slug='root'`, parent, workspace).Scan(&n)
	if n != 1 {
		t.Fatal("fork parent missing")
	}
	var pageFolder string
	_ = db.QueryRow(`SELECT folder_id FROM pages WHERE workspace_id=? AND slug='fleet-201'`, workspace).Scan(&pageFolder)
	if pageFolder != id {
		t.Fatal("fork page lost leaf membership")
	}
	var version int64
	_ = db.QueryRow(`SELECT version FROM page_folder_structure WHERE workspace_id=?`, workspace).Scan(&version)
	if version != treeVersion(t, f) {
		t.Fatal("fork tree fence changed")
	}
}

type folderBlockedBody struct {
	entered, release chan struct{}
	reader           io.Reader
}

func (b *folderBlockedBody) Read(p []byte) (int, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-b.release
	return b.reader.Read(p)
}
func (b *folderBlockedBody) Close() error { return nil }
func TestFolderTreeRechecksRevokedActorAfterBody(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "source", "engine", ""), 201)
	body := &folderBlockedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"parent":null,"tree_version":1}`)}
	r := f.request(t, "POST", "/", "mia", "MANAGER", "")
	r.SetPathValue("slug", "source")
	r.Body = body
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); f.h.ReparentFolder(w, r); done <- w }()
	<-body.entered
	execOrFatal(t, f.h.db, `DELETE FROM crew_members WHERE user_id='mia'`)
	close(body.release)
	artifactExpect(t, <-done, 404)
}

func TestFolderTreeMoveBudgetIncludesAllHiddenDescendants(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "budget-source", "engine", ""), 201)
	artifactExpect(t, treeCreate(t, f, "budget-target", "engine", ""), 201)
	source := folderID(t, f, "budget-source")
	tx, err := f.h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 256; i++ {
		_, err = tx.Exec(`INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,parent_id,created_at,updated_at) VALUES(?,?,?,?,?,?,'2026-10-15T00:00:00Z','2026-10-15T00:00:00Z')`, fmt.Sprintf("budget-child-%d", i), f.wsID, fmt.Sprintf("budget-child-%d", i), "Hidden child", "crew-lookout", source)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	w := treeMove(t, f, "budget-source", "budget-target", "mia", treeVersion(t, f))
	artifactExpect(t, w, 409)
	if !strings.Contains(w.Body.String(), "supported move budget") || strings.Contains(w.Body.String(), "Hidden child") || strings.Contains(w.Body.String(), "256") {
		t.Fatalf("budget refusal=%s", w.Body.String())
	}
	var parent sql.NullString
	if err = f.h.db.QueryRow(`SELECT parent_id FROM page_folders WHERE id=?`, source).Scan(&parent); err != nil || parent.Valid {
		t.Fatalf("refused move changed parent: %v %v", parent, err)
	}
}

func TestFolderTreeTargetACLAndStructureFencesAreNotRebased(t *testing.T) {
	f := newFoldersFixture(t)
	artifactExpect(t, treeCreate(t, f, "fenced-source", "engine", ""), 201)
	artifactExpect(t, treeCreate(t, f, "fenced-target", "engine", ""), 201)
	version := treeVersion(t, f)
	execOrFatal(t, f.h.db, `UPDATE page_folders SET acl_version=acl_version+1 WHERE id=?`, folderID(t, f, "fenced-target"))
	body := fmt.Sprintf(`{"parent":"fenced-target","tree_version":%d,"acl_version":0}`, version)
	r := f.request(t, "POST", "/api/v1/page-folders/fenced-source/parent", f.owner, "OWNER", body)
	r.SetPathValue("slug", "fenced-source")
	w := httptest.NewRecorder()
	f.h.ReparentFolder(w, r)
	artifactExpect(t, w, 409)
	artifactExpect(t, treeCreate(t, f, "other-change", "engine", ""), 201)
	artifactExpect(t, treeMove(t, f, "fenced-source", "fenced-target", f.owner, version), 409)
}

func TestFolderTreeRouterRechecksExactCredentialAfterBody(t *testing.T) {
	for _, test := range []struct{ name, kind, change string }{
		{"valid-session", "session", ""}, {"valid-cli", "cli", ""},
		{"revoked-session", "session", "revoked"}, {"revoked-cli", "cli", "revoked"},
		{"expired-session", "session", "expired"}, {"expired-cli", "cli", "expired"},
		{"cli-scope-change", "cli", "scope"},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind := test.kind
			f := newFoldersFixture(t)
			artifactExpect(t, treeCreate(t, f, "credential-source", "engine", ""), 201)
			artifactExpect(t, treeCreate(t, f, "credential-parent", "engine", ""), 201)
			const secret = "synthetic-folder-credential-secret-long-enough"
			validator, err := auth.NewJWTValidator(secret)
			if err != nil {
				t.Fatal(err)
			}
			var credential, credentialID string
			if kind == "session" {
				session, err := sessions.NewDBStore(f.h.db).Create(t.Context(), f.owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				credentialID = session.ID
				credential, err = validator.IssueAccessToken(f.owner, session.ID, "Synthetic", "synthetic@example.test")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				credential = "crewship_cli_folder_delayed_test"
				credentialID = "folder-delayed-cli"
				execOrFatal(t, f.h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,?,'synthetic delayed',?,'STANDARD','["workspace:admin"]','2026-10-15T00:00:00Z')`, credentialID, f.owner, hashStandard(credential))
			}
			router, err := NewRouter(f.h.db, secret, newTestLogger(), WithInternalToken("synthetic-folder-internal"), WithStoragePath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			body := &folderBlockedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(fmt.Sprintf(`{"parent":"credential-parent","tree_version":%d,"acl_version":0}`, treeVersion(t, f)))}
			t.Cleanup(func() {
				select {
				case <-body.release:
				default:
					close(body.release)
				}
			})
			request := httptest.NewRequest("POST", "/api/v1/page-folders/credential-source/parent?workspace_id="+f.wsID, body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+credential)
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); result <- w }()
			select {
			case <-body.entered:
			case w := <-result:
				t.Fatalf("credential never admitted to body: %d %s", w.Code, w.Body.String())
			case <-time.After(3 * time.Second):
				t.Fatal("body not reached")
			}
			table := "cli_tokens"
			if kind == "session" {
				table = "user_sessions"
			}
			switch test.change {
			case "revoked":
				execOrFatal(t, f.h.db, `UPDATE `+table+` SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, credentialID)
			case "expired":
				execOrFatal(t, f.h.db, `UPDATE `+table+` SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, credentialID)
			case "scope":
				execOrFatal(t, f.h.db, `UPDATE cli_tokens SET scopes='["pages:*"]' WHERE id=?`, credentialID)
			}
			close(body.release)
			var w *httptest.ResponseRecorder
			select {
			case w = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not finish after body release")
			}
			if test.change == "" {
				artifactExpect(t, w, 200)
			} else if w.Code != 401 && w.Code != 403 {
				t.Fatalf("changed %s credential accepted after middleware: %d %s", kind, w.Code, w.Body.String())
			}
			var parent sql.NullString
			if err = f.h.db.QueryRow(`SELECT parent_id FROM page_folders WHERE id=?`, folderID(t, f, "credential-source")).Scan(&parent); err != nil || parent.Valid != (test.change == "") {
				t.Fatalf("credential outcome changed parent incorrectly: %v %v", parent, err)
			}
		})
	}
}
