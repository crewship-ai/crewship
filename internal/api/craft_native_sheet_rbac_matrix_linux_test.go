package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Who may do what with a native Sheet, at the real API boundary: NewRouter,
// real credentials, workspace middleware, route gates, handlers and SQLite.
// Every cell states the status of every operation, allowed and refused alike.
// The engine's validator is the one injected piece; no permission helper is.

const (
	sheetRBACSecret = "synthetic-sheet-rbac-secret-32chars"
	sheetRBACPage   = "rbac-sheet"
	sheetRBACEmpty  = "rbac-empty"
)

type sheetRBAC struct {
	t      *testing.T
	f      *pagesTransferRig
	router *Router
	pin    string
	user   string
	bearer string
	// request scope of the caller
	workspace string
}

// The state of the document towards the caller.
const (
	sheetOwn          = "own private"
	sheetOthers       = "someone else's private"
	sheetSharedRead   = "shared to read"
	sheetSharedWrite  = "shared to write"
	sheetOtherCompany = "another workspace"
)

func newSheetRBAC(t *testing.T, role, mode, state string) *sheetRBAC {
	t.Helper()
	f, _, _ := craftNativeHTTPFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	s := &sheetRBAC{t: t, f: f, user: "rbac-caller", workspace: f.wsID}
	mustExec(t, f.db, `INSERT INTO users(id,email,full_name) VALUES('rbac-caller','rbac-caller@example.test','Caller'),('rbac-author','rbac-author@example.test','Author')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('rbac-author-member',?,'rbac-author','MEMBER')`, f.wsID)
	if state == sheetOtherCompany {
		mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('rbac-elsewhere','Elsewhere','rbac-elsewhere')`)
		s.workspace = "rbac-elsewhere"
	}
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('rbac-caller-member',?,'rbac-caller',?,?)`, s.workspace, role, mode)

	owner, private := "rbac-author", 0
	switch state {
	case sheetOwn:
		owner, private = s.user, 1
	case sheetOthers, sheetOtherCompany:
		private = 1
	}
	binary := filepath.Join(t.TempDir(), "fixture-cli")
	executable := []byte("#!/bin/sh\nexit 9\n")
	if err := os.WriteFile(binary, executable, 0700); err != nil {
		t.Fatal(err)
	}
	profile := CraftNativeDocumentProfile{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: binary, CLISHA256: fmt.Sprintf("%x", sha256.Sum256(executable)), EditorBundle: strings.Repeat("c", 64), EditorURL: "/_editor/b/" + strings.Repeat("c", 64) + "/index.html"}
	for _, id := range []string{sheetRBACPage, sheetRBACEmpty} {
		f.seedPage(t, id, id, id, owner, "")
		mustExec(t, f.db, `UPDATE pages SET private_audience=? WHERE id=?`, private, id)
		if level := map[string]string{sheetSharedRead: "read", sheetSharedWrite: "write"}[state]; level != "" {
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,?,'rbac-author')`, id, s.user, level)
		}
	}
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,'gridcraft',?,?,?)`, sheetRBACPage, f.wsID, profile.EngineRevision, profile.CLISHA256, profile.EditorBundle)
	router, err := NewRouter(f.db, sheetRBACSecret, newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]CraftNativeDocumentProfile{"gridcraft": profile}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	s.router = router
	app := router.craftNativeDocuments.applications["gridcraft"]
	app.Store.Validate = func(context.Context, []byte) error { return nil }
	router.craftNativeDocuments.applications["gridcraft"] = app
	router.craftNativeDocuments.importer().convert = okConvert
	s.pin = mooring.NewNativeStore(nil, app.Store).EnginePin()
	versions := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err = versions.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: sheetRBACPage}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":"SECRET"}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + owner, nil }); err != nil {
		t.Fatal(err)
	}
	if private == 0 {
		mustExec(t, f.db, `UPDATE artifact_version_heads SET shared_version=1 WHERE page_id=?`, sheetRBACPage)
	}
	for _, id := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		content := []byte(`{"v":"` + id + `"}`)
		mustExec(t, f.db, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) VALUES(?,?,?,'gridcraft',?,'agent:proposal-agent',?,1,'',?,?)`, id, sheetRBACPage, f.wsID, s.pin, id, fmt.Sprintf("sha256:%x", sha256.Sum256(content)), content)
	}
	s.bearer = mintTokenFor(t, f.db, s.user, "sheetrbac00000000000000000")
	return s
}

func (s *sheetRBAC) call(method, page, path, admission string, body any) *httptest.ResponseRecorder {
	s.t.Helper()
	var raw []byte
	if b, ok := body.([]byte); ok {
		raw = b
	} else if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			s.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/api/v1/pages/@"+page+"/craft/"+path+"?workspace_id="+s.workspace, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+s.bearer)
	req.Header.Set("Content-Type", "application/json")
	if admission != "" {
		req.Header.Set("X-Craft-Session", admission)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func (s *sheetRBAC) admit(page string) (token, id string, code int) {
	s.t.Helper()
	w := s.call("POST", page, "sessions", "", struct{}{})
	var out struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Token, out.ID, w.Code
}

func (s *sheetRBAC) body(session, generation string, base int64, raw []byte) map[string]any {
	return map[string]any{"workspaceId": s.f.wsID, "pageId": sheetRBACPage, "applicationId": "gridcraft", "enginePin": s.pin, "sessionId": session, "generation": generation, "baseVersion": strconv.FormatInt(base, 10), "editorRevision": "rbac:" + strconv.FormatInt(base, 10) + ":1", "contentHash": fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "bytes": raw}
}

func (s *sheetRBAC) head() (version int64) {
	s.t.Helper()
	if err := s.f.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, sheetRBACPage).Scan(&version); err != nil {
		s.t.Fatal(err)
	}
	return version
}

func (s *sheetRBAC) audits(actions ...string) (n int) {
	s.t.Helper()
	for _, action := range actions {
		var one int
		if err := s.f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE user_id=? AND action=?`, s.user, action).Scan(&one); err != nil {
			s.t.Fatal(err)
		}
		n += one
	}
	return n
}

// run makes every call once, in the order a person would, and returns the
// status of each. A refused call must release nothing of the document.
func (s *sheetRBAC) run() map[string]int {
	s.t.Helper()
	got := map[string]int{}
	token, session, code := s.admit(sheetRBACPage)
	got["session"] = code
	if token == "" {
		token = strings.Repeat("0", 64)
		session = "none"
	}
	record := func(name string, w *httptest.ResponseRecorder) {
		got[name] = w.Code
		if w.Code >= 300 && (strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "U0VDUkVU")) {
			s.t.Errorf("%s refused with %d but released content", name, w.Code)
		}
	}
	first, second := strings.Repeat("1", 32), strings.Repeat("2", 32)
	record("read", s.call("GET", sheetRBACPage, "document", token, nil))
	record("versions", s.call("GET", sheetRBACPage, "document/versions", token, nil))
	record("version", s.call("GET", sheetRBACPage, "document/versions/1", token, nil))
	record("proposals", s.call("GET", sheetRBACPage, "proposals", token, nil))
	record("preview", s.call("GET", sheetRBACPage, "preview", "", nil))
	acquired := s.call("POST", sheetRBACPage, "lease/acquire", token, struct{}{})
	record("acquire", acquired)
	generation := "1"
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquired.Code == 200 && json.Unmarshal(acquired.Body.Bytes(), &lease) == nil {
		generation = lease.Generation
	}
	record("reject", s.call("POST", sheetRBACPage, "proposals/"+first+"/reject", token, struct{}{}))
	record("accept", s.call("POST", sheetRBACPage, "proposals/"+second+"/accept", token, map[string]string{"sessionId": session, "generation": generation}))
	base := s.head()
	record("save", s.call("POST", sheetRBACPage, "document/save", token, s.body(session, generation, base, []byte(`{"v":"saved"}`))))
	base = s.head()
	restore := s.body(session, generation, base, nil)
	restore["targetVersion"] = "1"
	record("restore", s.call("POST", sheetRBACPage, "document/restore", token, restore))
	base = s.head()
	record("share", s.call("POST", sheetRBACPage, "document/share", token, map[string]string{"workspaceId": s.f.wsID, "pageId": sheetRBACPage, "applicationId": "gridcraft", "enginePin": s.pin, "sessionId": session, "generation": generation, "baseVersion": strconv.FormatInt(base, 10), "targetVersion": strconv.FormatInt(base, 10)}))
	record("release", s.call("POST", sheetRBACPage, "lease/release", token, map[string]string{"generation": generation}))
	record("import", s.call("POST", sheetRBACEmpty, "import", "", []byte("PK synthetic workbook")))
	record("initialize", s.call("POST", sheetRBACEmpty, "initialize", "", map[string]any{"application_id": "gridcraft", "bytes": []byte(`{"v":"new"}`)}))
	return got
}

var sheetRBACOperations = []string{"session", "read", "versions", "version", "proposals", "preview", "acquire", "reject", "accept", "save", "restore", "share", "release", "import", "initialize"}

// sheetRBACRow spells a row in the order of sheetRBACOperations.
func sheetRBACRow(t *testing.T, codes string) map[string]int {
	t.Helper()
	fields := strings.Fields(codes)
	if len(fields) != len(sheetRBACOperations) {
		t.Fatalf("%d codes for %d operations", len(fields), len(sheetRBACOperations))
	}
	row := map[string]int{}
	for i, field := range fields {
		row[sheetRBACOperations[i]], _ = strconv.Atoi(field)
	}
	return row
}

func TestCraftSheetRBACMatrix(t *testing.T) {
	//                  session read versions version proposals preview acquire reject accept save restore share release import initialize
	const (
		owner       = "201 200 200 200 200 404 200 200 200 200 200 200 200 202 201"
		editor      = "201 200 200 200 200 200 200 200 200 200 200 403 200 403 403"
		editPrivate = "201 200 200 200 200 404 200 200 200 200 200 403 200 403 403"
		adminReads  = "201 200 200 200 200 404 403 403 403 403 403 403 403 403 403"
		previewOnly = "201 403 403 403 403 200 403 403 403 403 403 403 403 403 403"
		nothing     = "403 403 403 403 403 403 403 403 403 403 403 403 403 403 403"
		guest       = "404 404 404 404 404 404 404 404 404 404 404 404 404 404 404"
	)
	type cell struct {
		role, mode, state, want string
		// readAudited: another person's document reached only through the role.
		readAudited bool
	}
	var cells []cell
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		manages := role == "OWNER" || role == "ADMIN"
		others, sharedRead := cell{role, "trusted", sheetOthers, nothing, false}, cell{role, "trusted", sheetSharedRead, previewOnly, false}
		if role == "ADMIN" {
			others.want, others.readAudited = adminReads, true
		}
		if role == "OWNER" {
			// Today's behaviour: the workspace OWNER is a custodian of every private
			// page for writing too (privatePageCustodian).
			others.want, others.readAudited = editPrivate, true
		}
		if manages {
			// A manager of the workspace edits every shared page.
			sharedRead.want = editor
		}
		cells = append(cells,
			cell{role, "trusted", sheetOwn, owner, false},
			others, sharedRead,
			cell{role, "trusted", sheetSharedWrite, editor, false},
			cell{role, "trusted", sheetOtherCompany, nothing, false},
		)
	}
	for _, state := range []string{sheetOwn, sheetSharedRead, sheetSharedWrite} {
		cells = append(cells, cell{"MEMBER", "restricted", state, guest, false})
	}
	for _, c := range cells {
		t.Run(c.role+"/"+c.mode+"/"+c.state, func(t *testing.T) {
			s := newSheetRBAC(t, c.role, c.mode, c.state)
			got, want := s.run(), sheetRBACRow(t, c.want)
			for _, op := range sheetRBACOperations {
				if got[op] != want[op] {
					t.Errorf("%-10s %d, want %d", op, got[op], want[op])
				}
			}
			if want["save"] != 200 && s.head() != 1 {
				t.Errorf("a caller who may not write moved the document to version %d", s.head())
			}
			if n := s.audits(auditActionPrivateArtifactRead, auditActionWorkspaceAdminArtifactRead); (n > 0) != c.readAudited {
				t.Errorf("%d administrator read audits, want any: %v", n, c.readAudited)
			}
		})
	}
}

// A document the caller cannot see answers exactly like one that does not exist.
func TestCraftSheetRBACUnseenEqualsMissing(t *testing.T) {
	for _, state := range []string{sheetOthers, sheetOtherCompany} {
		t.Run(state, func(t *testing.T) {
			s := newSheetRBAC(t, "MEMBER", "trusted", state)
			for _, op := range []struct {
				method, path string
				body         any
			}{
				{"GET", "availability", nil}, {"GET", "preview", nil}, {"POST", "sessions", struct{}{}},
				{"POST", "import", []byte("PK")}, {"POST", "initialize", map[string]any{"application_id": "gridcraft", "bytes": []byte(`{}`)}},
				{"GET", "import/" + strings.Repeat("a", 64), nil},
			} {
				seen := s.call(op.method, sheetRBACPage, op.path, "", op.body)
				missing := s.call(op.method, "rbac-missing", op.path, "", op.body)
				if seen.Code != missing.Code || seen.Body.String() != missing.Body.String() {
					t.Errorf("%s %s: unseen %d %s, missing %d %s", op.method, op.path, seen.Code, seen.Body, missing.Code, missing.Body)
				}
			}
		})
	}
}

// The credential: a CLI token's scopes, a browser session, and an agent's run
// token, which is never a person on these routes.
func TestCraftSheetRBACCredentials(t *testing.T) {
	for name, tc := range map[string]struct {
		scope      string
		read, save int
	}{
		"pages:read token":  {"pages:read", 200, 403},
		"pages:write token": {"pages:write", 403, 403},
		"pages:* token":     {"pages:*", 200, 200},
		"agents:* token":    {"agents:*", 403, 403},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSheetRBAC(t, "MEMBER", "trusted", sheetOwn)
			s.bearer = mintIngressScopedToken(t, s.f.db, s.user, "sheet-rbac", tc.scope)
			if got := s.run(); got["read"] != tc.read || got["save"] != tc.save {
				t.Fatalf("read %d save %d, want %d %d (%v)", got["read"], got["save"], tc.read, tc.save, got)
			}
		})
	}
	t.Run("browser session", func(t *testing.T) {
		s := newSheetRBAC(t, "MEMBER", "trusted", sheetOwn)
		validator, err := auth.NewJWTValidator(sheetRBACSecret)
		if err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(s.f.db).Create(t.Context(), s.user, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		if s.bearer, err = validator.IssueAccessToken(s.user, session.ID, "Caller", "rbac-caller@example.test"); err != nil {
			t.Fatal(err)
		}
		if got := s.run(); got["read"] != 200 || got["save"] != 200 {
			t.Fatalf("%v", got)
		}
	})
	t.Run("agent run token", func(t *testing.T) {
		s := newSheetRBAC(t, "MEMBER", "trusted", sheetOwn)
		key := internaltoken.DeriveAgentRunKey(sheetRBACSecret, s.f.wsID, "some-crew")
		s.bearer = internaltoken.DeriveAgentRunToken(key, s.f.wsID, "some-agent", "some-run")
		for op, code := range s.run() {
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("%s answered an agent's run token with %d", op, code)
			}
		}
	})
}

// Taking a right away ends the open editor: the holder's next save is refused
// and the document stays as it was.
func TestCraftSheetRBACRevokedWhileEditing(t *testing.T) {
	for name, revoke := range map[string]string{
		"membership removed": `DELETE FROM workspace_members WHERE id='rbac-caller-member'`,
		"made a guest":       `UPDATE workspace_members SET access_mode='restricted' WHERE id='rbac-caller-member'`,
		"account suspended":  `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id='rbac-caller'`,
		"grant removed":      `DELETE FROM page_grants WHERE subject_id='rbac-caller'`,
		"grant lowered":      `UPDATE page_grants SET level='read' WHERE subject_id='rbac-caller'`,
		"token revoked":      `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id='rbac-caller'`,
		"page made private":  `UPDATE pages SET private_audience=1 WHERE id='rbac-sheet'`,
		"role changed":       `UPDATE workspace_members SET role='VIEWER' WHERE id='rbac-caller-member'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSheetRBAC(t, "MEMBER", "trusted", sheetSharedWrite)
			token, session, _ := s.admit(sheetRBACPage)
			w := s.call("POST", sheetRBACPage, "lease/acquire", token, struct{}{})
			var lease struct {
				Generation string `json:"generation"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lease) != nil {
				t.Fatalf("calibration acquire: %d %s", w.Code, w.Body)
			}
			if w = s.call("POST", sheetRBACPage, "document/save", token, s.body(session, lease.Generation, 1, []byte(`{"v":"before"}`))); w.Code != 200 {
				t.Fatalf("calibration save: %d %s", w.Code, w.Body)
			}
			mustExec(t, s.f.db, revoke)
			if w = s.call("POST", sheetRBACPage, "document/save", token, s.body(session, lease.Generation, 2, []byte(`{"v":"after"}`))); w.Code != 401 && w.Code != 403 && w.Code != 404 {
				t.Errorf("save after the right was taken away: %d %s", w.Code, w.Body)
			}
			if s.head() != 2 {
				t.Errorf("the document moved to version %d", s.head())
			}
			if w = s.call("GET", sheetRBACPage, "document", token, nil); w.Code < 300 {
				t.Errorf("the editor's session still reads the document: %d", w.Code)
			}
		})
	}
}

// Another person's import is the same 404 as an import that does not exist.
func TestCraftSheetRBACImportBelongsToItsOwner(t *testing.T) {
	s := newSheetRBAC(t, "MEMBER", "trusted", sheetOwn)
	w := s.call("POST", sheetRBACEmpty, "import", "", []byte("PK synthetic workbook"))
	var job struct {
		ID string `json:"id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.ID == "" {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if w = s.call("GET", sheetRBACEmpty, "import/"+job.ID, "", nil); strings.Contains(w.Body.String(), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the import never finished: %d %s", w.Code, w.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, other := range []string{"rbac-author", s.f.adminID} { // a member, and the workspace OWNER
		s.bearer = mintTokenFor(t, s.f.db, other, "sheetrbacother"+other)
		for _, op := range []struct{ method, path string }{{"GET", ""}, {"GET", "/document"}, {"POST", "/initialize"}} {
			real := s.call(op.method, sheetRBACEmpty, "import/"+job.ID+op.path, "", nil)
			unknown := s.call(op.method, sheetRBACEmpty, "import/"+strings.Repeat("a", len(job.ID))+op.path, "", nil)
			if real.Code != 404 || real.Body.String() != unknown.Body.String() {
				t.Errorf("%s %s import%s: %d %s, unknown %d %s", other, op.method, op.path, real.Code, real.Body, unknown.Code, unknown.Body)
			}
		}
	}
}

// A reader who opened the Sheet keeps nothing new once the right is gone: no
// document, no version list, no older version, no preview and no new session
// from the page they still have open. A copy already downloaded (the editor's
// own xlsx export of what it read) is outside the server; a new release is not.
func TestCraftSheetRBACRevokedReaderGetsNoNewContent(t *testing.T) {
	for name, revoke := range map[string]string{
		"grant removed":      `DELETE FROM page_grants WHERE subject_id='rbac-caller'`,
		"membership removed": `DELETE FROM workspace_members WHERE id='rbac-caller-member'`,
		"made a guest":       `UPDATE workspace_members SET access_mode='restricted' WHERE id='rbac-caller-member'`,
		"account suspended":  `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id='rbac-caller'`,
		"token revoked":      `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id='rbac-caller'`,
		"page made private":  `UPDATE pages SET private_audience=1 WHERE id='rbac-sheet'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSheetRBAC(t, "MEMBER", "trusted", sheetSharedRead)
			token, _, code := s.admit(sheetRBACPage)
			released := func(w *httptest.ResponseRecorder) bool {
				return strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "U0VDUkVU")
			}
			before := map[string]int{}
			for _, path := range []string{"document", "document/versions", "document/versions/1", "preview"} {
				admission := token
				if path == "preview" {
					admission = ""
				}
				before[path] = s.call("GET", sheetRBACPage, path, admission, nil).Code
			}
			// A read grant releases the shared version (preview) only; the private
			// document and its history are refused before the change already.
			if before["preview"] != 200 || before["document"] < 300 || before["document/versions"] < 300 {
				t.Fatalf("calibration: %v", before)
			}
			version := s.head()
			mustExec(t, s.f.db, revoke)
			for _, path := range []string{"document", "document/versions", "document/versions/1", "preview"} {
				admission := token
				if path == "preview" {
					admission = ""
				}
				if w := s.call("GET", sheetRBACPage, path, admission, nil); w.Code < 300 || released(w) {
					t.Errorf("%s after the right was taken away: %d (before %d)", path, w.Code, before[path])
				}
			}
			if _, _, after := s.admit(sheetRBACPage); after < 300 && code >= 300 {
				t.Errorf("a new session after the right was taken away: %d (before %d)", after, code)
			} else if after < 300 {
				t.Errorf("a new session after the right was taken away: %d", after)
			}
			if s.head() != version {
				t.Errorf("the document moved to version %d", s.head())
			}
		})
	}
}

// agent-access names the latest revision only to someone who may read it: a
// write grantee does, a read grantee (shared version only) does not.
func TestCraftSheetRBACAgentAccessHeadOnlyForPrivateReaders(t *testing.T) {
	for state, want := range map[string]bool{sheetSharedRead: false, sheetSharedWrite: true} {
		t.Run(state, func(t *testing.T) {
			s := newSheetRBAC(t, "MEMBER", "trusted", state)
			req := httptest.NewRequest("GET", "/api/v1/pages/@"+sheetRBACPage+"/craft/agent-access?chat_id=rbac-chat&workspace_id="+s.workspace, nil)
			req.Header.Set("Authorization", "Bearer "+s.bearer)
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("agent access: %d %s", w.Code, w.Body)
			}
			var out craftNativeAgentAccessResponse
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if got := out.HeadVersion != "" || out.SavedAt != "" || out.SavedByName != ""; got != want {
				t.Fatalf("head given %v, want %v: %s", got, want, w.Body)
			}
			if want && out.HeadVersion != strconv.FormatInt(s.head(), 10) {
				t.Fatalf("head_version %q, head %d", out.HeadVersion, s.head())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("agent access may be cached")
			}
		})
	}
}

// The owner's own path over the router with their CLI login token, as the web
// client and the acceptance test take it: import, wait, then initialize the
// empty page from the import id with no body.
func TestCraftSheetRBACOwnerInitializesFromTheirImport(t *testing.T) {
	s := newSheetRBAC(t, "MEMBER", "trusted", sheetOwn)
	w := s.call("POST", sheetRBACEmpty, "import", "", []byte("PK synthetic workbook"))
	var job struct {
		ID string `json:"id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.ID == "" {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.call("GET", sheetRBACEmpty, "import/"+job.ID, "", nil).Body.String(), "ready") {
		if time.Now().After(deadline) {
			t.Fatal("the import never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	req := httptest.NewRequest("POST", "/api/v1/pages/@"+sheetRBACEmpty+"/craft/import/"+job.ID+"/initialize?workspace_id="+s.workspace, nil)
	req.Header.Set("Authorization", "Bearer "+s.bearer)
	out := httptest.NewRecorder()
	s.router.ServeHTTP(out, req)
	if out.Code != 201 {
		t.Fatalf("initialize from the owner's import: %d %s", out.Code, out.Body)
	}
}
