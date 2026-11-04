package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Security PRD §4 gap 7 (SEC-AUD-02): an OWNER/ADMIN read of another member's
// artifact commits its audit row before any content is released, so a failed
// audit insert releases nothing. Every case first calibrates that the same
// read succeeds and is audited without the fault, so a refusal under the
// fault is the audit gate and not an unrelated 404.

const rbacDenyAudit = `CREATE TRIGGER rbac_deny_admin_read_audit BEFORE INSERT ON audit_logs
WHEN NEW.action IN ('artifact.private_owner_accessed','artifact.workspace_admin_accessed')
BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`

func rbacAuditGate(t *testing.T, db *sql.DB, name string, read func() *httptest.ResponseRecorder, audits func() int, secrets ...string) {
	t.Helper()
	before := audits()
	ok := read()
	if ok.Code != 200 || audits() != before+1 {
		t.Fatalf("%s calibration: HTTP %d audits %d->%d: %.200s", name, ok.Code, before, audits(), ok.Body.String())
	}
	found := false
	for _, s := range secrets {
		found = found || strings.Contains(ok.Body.String(), s)
	}
	if !found {
		t.Fatalf("%s calibration body misses every marker %v: %.400s", name, secrets, ok.Body.String())
	}
	mustExec(t, db, rbacDenyAudit)
	defer mustExec(t, db, `DROP TRIGGER rbac_deny_admin_read_audit`)
	before = audits()
	got := read()
	if got.Code == 200 || got.Code < 500 {
		t.Errorf("%s with failing audit: HTTP %d, want a 5xx refusal: %.200s", name, got.Code, got.Body.String())
	}
	for _, s := range secrets {
		if strings.Contains(got.Body.String(), s) {
			t.Errorf("%s released %q without its audit row: %.300s", name, s, got.Body.String())
		}
	}
	if n := audits(); n != before {
		t.Errorf("%s audit rows changed %d->%d under the fault", name, before, n)
	}
}

func TestWorkspaceAdminArtifactReadAuditFailureReleasesNothing(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, user := range []string{"wsowner", "wsadmin"} {
			t.Run(fmt.Sprintf("project-shared=%v/%s", shared, user), func(t *testing.T) {
				m := newRBACMatrix(t, shared)
				audits := func() int { return m.audits(user) }
				for _, path := range []string{
					"/api/v1/pages/matrix/artifact",
					"/api/v1/pages/matrix/artifact?version=1",
					"/api/v1/pages/matrix/artifact/versions",
					"/api/v1/pages/matrix/artifact/comments",
				} {
					rbacAuditGate(t, m.f.db, path, func() *httptest.ResponseRecorder { return m.do(user, "GET", path, "") }, audits, "SECRET-V1", "SECRET-V2", "SECRET-V3")
				}
				// Dashboard data: the rows, not the code, are the secret.
				path := "/api/v1/pages/matrix/artifact/data/sales?version=1"
				rbacAuditGate(t, m.f.db, "dashboard data "+path, func() *httptest.ResponseRecorder { return m.do(user, "GET", path, "") }, audits, "4242")
				list := "/api/v1/pages/matrix/artifact/data?version=1"
				rbacAuditGate(t, m.f.db, "dashboard collections "+list, func() *httptest.ResponseRecorder { return m.do(user, "GET", list, "") }, audits, "sales")
			})
		}
	}
	t.Run("slides", func(t *testing.T) {
		m := newRBACMatrix(t, true)
		mustExec(t, m.f.db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id='author'`)
		if c := m.do("author", "POST", "/api/v1/pages/artifacts", `{"slug":"deck","name":"Deck",`+strings.TrimPrefix(slidesArtifactBody(0, "SLIDE-SECRET-ONE"), "{")); c.Code != 201 {
			t.Fatalf("slides create %d %s", c.Code, c.Body)
		}
		if c := m.do("author", "POST", "/api/v1/pages/deck/artifact/share", `{"base_version":1,"version":1}`); c.Code != 200 {
			t.Fatalf("slides share %d %s", c.Code, c.Body)
		}
		if c := m.do("author", "POST", "/api/v1/pages/deck/artifact/versions", slidesArtifactBody(1, "SLIDE-SECRET-TWO")); c.Code != 200 {
			t.Fatalf("slides v2 %d %s", c.Code, c.Body)
		}
		mustExec(t, m.f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='author'`)
		for _, user := range []string{"wsowner", "wsadmin"} {
			audits := func() int { return m.audits(user) }
			for _, v := range []string{"1", "2"} {
				path := "/api/v1/pages/deck/artifact/slides?version=" + v
				rbacAuditGate(t, m.f.db, user+" "+path, func() *httptest.ResponseRecorder { return m.do(user, "GET", path, "") }, audits, "SLIDE-SECRET-ONE", "SLIDE-SECRET-TWO")
			}
			// The PDF encodes text, so the gate is the status and the PDF magic.
			path := "/api/v1/pages/deck/artifact/export.pdf?version=2"
			rbacAuditGate(t, m.f.db, user+" "+path, func() *httptest.ResponseRecorder { return m.do(user, "GET", path, "") }, audits, "%PDF-")
		}
	})
	t.Run("native-word", func(t *testing.T) {
		// Native Craft reads need an editor-origin session token minted by the
		// Craft session handler; this drives the real handlers and store the
		// same way TestWorkspaceAdminNativeWordReadAuditSurvivesConcurrentCustodyTransfer
		// does, with no permission helper replaced.
		f, h, _ := craftNativeHTTPFixture(t)
		mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
		mustExec(t, f.db, `UPDATE pages SET owner_user_id=?, private_audience=1 WHERE id='native-page'`, f.adminID)
		mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('rbac-native-admin',?,?)`, f.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		u := &AuthUser{ID: f.userID, SessionID: "rbac-native-admin", CredentialExpiresAt: time.Now().Add(time.Hour)}
		source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
		if err != nil {
			t.Fatal(err)
		}
		u.OriginalCredential = source
		ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
		cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
		d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"wordcraft": {Store: cfg, EditorURL: "/workbook-editor/b/" + cfg.EditorBundle + "/index.html"}})
		s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
		nativeDoc := []byte(`{"secret":"NATIVE-SECRET"}`)
		if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: nativeDoc, MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.adminID, nil }); err != nil {
			t.Fatal(err)
		}
		token, _ := craftNativeIssued(t, h, ctx)
		audits := func() int {
			return adminReadAudits(t, h.pages, auditActionPrivateArtifactRead, f.userID, "native-page")
		}
		rbacAuditGate(t, f.db, "native Word read", func() *httptest.ResponseRecorder { return craftNativeDocumentCall(t, d, ctx, token, "read", nil) }, audits, "NATIVE-SECRET", base64.StdEncoding.EncodeToString(nativeDoc)) // the read carries the bytes base64-encoded
	})
	t.Run("sheet-named-editor-history", func(t *testing.T) {
		// Codex non-blocking note on #4839: the ADMIN named as a Sheet editor
		// reads the retained history through the administrator path; that read
		// is audited and must fail closed too.
		f := gridcraftInstanceFixture(t)
		f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
		setGridCraftEditorFlag(t, f, "ws-old", true)
		seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
		seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
		ann := mintTokenFor(t, f.db, "ann", "rbacsheetann000000000000")
		base := liveWorkbook(t, f, ann, "ann", "rbac-history")
		owner := openEditorSession(t, f, ann, base)
		wantCode(t, f.do(ann, "POST", base+"/edits"+rbacQ, cellEdit(owner.SessionID, 1, 1, "B2", "private-draft")), 200, "owner private edit")
		wantCode(t, f.do(ann, "POST", base+"/editors"+rbacQ, `{"editor":"bob@ex.com","publish":true}`), 200, "publish with bob")
		wantCode(t, f.do(ann, "POST", base+"/editors"+rbacQ, `{"editor":"wsadmin@ex.com"}`), 200, "name the ADMIN editor")
		audits := func() int {
			return adminReadAudits(t, f.r.pages, auditActionPrivateArtifactRead, "wsadmin", "rbac-history")
		}
		rbacAuditGate(t, f.db, "named-editor ADMIN Sheet history", func() *httptest.ResponseRecorder {
			return f.do(f.wsAdmin, "GET", base+"/commits"+rbacQ+"&since=0", "")
		}, audits, "private-draft")
	})
}

// (d) Revocation takes effect on the next request: grant removal through the
// grants API, membership removal and demotion through the members API, and a
// custody change. Nothing is cached across requests.
func TestArtifactRBACRevocationTakesEffectOnNextRequest(t *testing.T) {
	t.Run("grant-revoked", func(t *testing.T) {
		m := newRBACMatrix(t, true)
		for _, user := range []string{"reader", "commenter", "editor"} {
			if got := m.do(user, "GET", "/api/v1/pages/matrix/artifact", ""); got.Code != 200 {
				t.Fatalf("%s calibration %d", user, got.Code)
			}
			if got := m.do("author", "DELETE", "/api/v1/pages/matrix/grants?subject_type=user&subject="+user, ""); got.Code != 200 && got.Code != 204 {
				t.Fatalf("revoke %s: %d %s", user, got.Code, got.Body)
			}
			for _, path := range []string{"/api/v1/pages/matrix/artifact", "/api/v1/pages/matrix/artifact/comments", "/api/v1/pages/matrix/artifact/data/sales?version=2"} {
				got := m.do(user, "GET", path, "")
				if got.Code != 404 || rbacMarkers(got.Body.String()) != "" || strings.Contains(got.Body.String(), "4242") {
					t.Errorf("%s after revocation %s: %d %.200s", user, path, got.Code, got.Body.String())
				}
			}
			if got := m.do(user, "POST", "/api/v1/pages/matrix/artifact/comments", rbacComment(2)); got.Code != 404 {
				t.Errorf("%s comment after revocation: %d", user, got.Code)
			}
		}
	})
	t.Run("membership-removed", func(t *testing.T) {
		m := newRBACMatrix(t, true)
		if got := m.do("reader", "GET", "/api/v1/pages/matrix/artifact", ""); got.Code != 200 {
			t.Fatalf("calibration %d", got.Code)
		}
		if got := m.do("wsowner", "DELETE", "/api/v1/workspaces/"+rbacWS+"/members/m-reader-"+rbacWS, ""); got.Code/100 != 2 {
			t.Fatalf("remove member: %d %s", got.Code, got.Body)
		}
		got := m.do("reader", "GET", "/api/v1/pages/matrix/artifact", "")
		if got.Code == 200 || rbacMarkers(got.Body.String()) != "" {
			t.Errorf("removed member still reads: %d %.200s", got.Code, got.Body.String())
		}
	})
	t.Run("admin-demoted", func(t *testing.T) {
		m := newRBACMatrix(t, false)
		if got := m.do("wsadmin", "GET", "/api/v1/pages/matrix/artifact?version=1", ""); got.Code != 200 || m.audits("wsadmin") != 1 {
			t.Fatalf("calibration %d audits %d", got.Code, m.audits("wsadmin"))
		}
		if got := m.do("wsowner", "PATCH", "/api/v1/workspaces/"+rbacWS+"/members/m-wsadmin-"+rbacWS, `{"role":"MEMBER"}`); got.Code/100 != 2 {
			t.Fatalf("demote: %d %s", got.Code, got.Body)
		}
		for _, path := range []string{"/api/v1/pages/matrix/artifact", "/api/v1/pages/matrix/artifact?version=1", "/api/v1/pages/matrix/artifact/data/sales?version=1"} {
			got := m.do("wsadmin", "GET", path, "")
			if got.Code != 404 || rbacMarkers(got.Body.String()) != "" || strings.Contains(got.Body.String(), "4242") {
				t.Errorf("demoted ADMIN %s: %d %.200s", path, got.Code, got.Body.String())
			}
		}
		if n := m.audits("wsadmin"); n != 1 {
			t.Errorf("refused reads after demotion were audited: %d rows", n)
		}
	})
	t.Run("custody-changed", func(t *testing.T) {
		m := newRBACMatrix(t, false)
		if got := m.do("member", "GET", "/api/v1/pages/matrix/artifact", ""); got.Code != 404 {
			t.Fatalf("calibration member %d", got.Code)
		}
		// Ownership succession as the members/pages transfer writes it.
		mustExec(t, m.f.db, `UPDATE pages SET owner_user_id='member' WHERE id=?`, m.page)
		if got := m.do("author", "GET", "/api/v1/pages/matrix/artifact", ""); got.Code != 404 || rbacMarkers(got.Body.String()) != "" {
			t.Errorf("former custodian still reads private output: %d %.200s", got.Code, got.Body.String())
		}
		if got := m.do("author", "POST", "/api/v1/pages/matrix/artifact/versions", rbacProjectBody(2, 8)); got.Code != 404 && got.Code != 403 {
			t.Errorf("former custodian still publishes: %d", got.Code)
		}
		got := m.do("member", "GET", "/api/v1/pages/matrix/artifact", "")
		if got.Code != 200 || rbacMarkers(got.Body.String()) != "2" {
			t.Errorf("new custodian read: %d %.200s", got.Code, got.Body.String())
		}
		if n := m.audits("member"); n != 0 {
			t.Errorf("custodian's own read audited as administrator access: %d", n)
		}
	})
}
