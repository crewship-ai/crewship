package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the real agent creation transaction with a MEMBER as its human.
// Node computation alone is substituted by documentCreationFixture.
func TestPrivateChatDocumentAudience(t *testing.T) {
	h, workspace, author, _, create := documentCreationFixture(t)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, author)
	creationExpect(t, create("member", documentCreationBody), 201)
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		pagesSeedUser(t, h, workspace, "private-"+role, strings.ToLower(role)+"@example.com", role)
	}
	for _, tc := range []struct {
		name, user, role string
		allowed          bool
	}{
		{"author", author, "MEMBER", true},
		{"owner", "private-OWNER", "OWNER", true},
		{"admin", "private-ADMIN", "ADMIN", false},
		{"manager", "private-MANAGER", "MANAGER", false},
		{"member", "private-MEMBER", "MEMBER", false},
		{"viewer", "private-VIEWER", "VIEWER", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := pagesGetStatus(t, h, workspace, tc.user, tc.role, "created-doc")
			want := 404
			if tc.allowed {
				want = 200
			}
			if get.Code != want {
				t.Errorf("direct access=%d want%d: %s", get.Code, want, get.Body.String())
			}
			listed := pagesSlugListed(pagesListSlugs(t, h, workspace, tc.user, tc.role), "created-doc")
			if listed != tc.allowed {
				t.Errorf("catalog visible=%v want%v", listed, tc.allowed)
			}
			if !tc.allowed && strings.Contains(get.Body.String(), "Created report") {
				t.Error("private title disclosed")
			}
		})
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id='private-OWNER'`).Scan(&count); err != nil || count == 0 {
		t.Errorf("OWNER access not durably audited: count%d err%v", count, err)
	}
	access := pagesAccessCall(t, h, workspace, author, "MEMBER", "created-doc", "")
	if access.Code != 200 {
		t.Fatal(access.Body.String())
	}
	var report pagesAccessDoc
	if err := json.Unmarshal(access.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(access.Body.String(), "admin@example.com") {
		t.Error("access report invents an ADMIN path")
	}
	denied := pagesAccessCall(t, h, workspace, "private-ADMIN", "ADMIN", "created-doc", "")
	if denied.Code != 404 {
		t.Errorf("ADMIN access report=%d want404", denied.Code)
	}
	for _, subject := range []string{author, "private-OWNER"} {
		r := pagesSubjectAccessCall(t, h, workspace, "private-ADMIN", "ADMIN", "subject=user:"+subject)
		if r.Code != 200 || strings.Contains(r.Body.String(), "created-doc") || strings.Contains(r.Body.String(), "Created report") {
			t.Errorf("ADMIN subject access disclosed private artifact: %d %s", r.Code, r.Body.String())
		}
	}
	// An unavailable audit must not disclose another person's private document.
	execOrFatal(t, h.db, `CREATE TRIGGER reject_private_access_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	r := pagesGetStatus(t, h, workspace, "private-OWNER", "OWNER", "created-doc")
	if r.Code != 500 || strings.Contains(r.Body.String(), "Created report") {
		t.Errorf("audit failure returned private metadata: %d %s", r.Code, r.Body.String())
	}
}

func TestPrivateAudienceLegacyPublicLinkAndOverview(t *testing.T) {
	public, _, _, workspace, owner := newPagesPublicFixture(t, "private-after-classification")
	link := pagesPublish(t, public, workspace, owner, "private-after-classification", `{}`)
	execOrFatal(t, public.db, `UPDATE pages SET private_audience=1 WHERE slug='private-after-classification'`)
	response := pagesPublicGet(t, public, link["token"].(string))
	if response.Code != 404 {
		t.Errorf("legacy public link disclosed private page: %d", response.Code)
	}
	h, workspace, _, _, create := documentCreationFixture(t)
	creationExpect(t, create("private", documentCreationBody), 201)
	r := httptest.NewRequest("GET", "/api/v1/internal/workspace/overview?workspace_id="+workspace, nil)
	w := httptest.NewRecorder()
	(&InternalHandler{db: h.db, logger: h.logger}).WorkspaceOverview(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Created report") || strings.Contains(w.Body.String(), "created-doc") {
		t.Errorf("crew overview disclosed private title: %d %s", w.Code, w.Body.String())
	}
}

func TestPrivateAudienceCustodyAndAuditCatalog(t *testing.T) {
	r, handler := ownershipFixture(t, "documents")
	execOrFatal(t, r.db, `UPDATE pages SET private_audience=1 WHERE id='private-output'`)
	execOrFatal(t, r.db, `UPDATE workspace_members SET role='ADMIN' WHERE user_id=? AND workspace_id=?`, r.adminID, r.wsID)
	denied := ownershipCall(t, r, handler, "GET", "/", "")
	if denied.Code != 200 || strings.Contains(denied.Body.String(), "private-output") {
		t.Fatalf("ADMIN custody catalog disclosed private metadata: %s", denied.Body.String())
	}
	execOrFatal(t, r.db, `UPDATE workspace_members SET role='OWNER' WHERE user_id=? AND workspace_id=?`, r.adminID, r.wsID)
	transfer := ownershipCall(t, r, handler, "POST", "/", `{"expected_owner_user_id":"`+r.userID+`","successor_user_id":"ungranted"}`)
	if transfer.Code != 200 {
		t.Fatalf("OWNER cannot select new MEMBER custodian: %d %s", transfer.Code, transfer.Body.String())
	}
	h := NewPageHandler(r.db, nil, newTestLogger())
	w := pagesGetStatus(t, h, r.wsID, r.adminID, "OWNER", "private-output")
	if w.Code != 200 {
		t.Fatalf("OWNER opening successor document: %d %s", w.Code, w.Body.String())
	}
	execOrFatal(t, r.db, `UPDATE workspace_members SET role='ADMIN' WHERE user_id=? AND workspace_id=?`, r.adminID, r.wsID)
	// A stale role must not preserve discovery or expose the access audit.
	if pagesSlugListed(pagesListSlugs(t, h, r.wsID, r.adminID, "OWNER"), "private-output") {
		t.Error("stale OWNER role disclosed private catalog entry")
	}
	audit := NewAuditHandler(r.db, newTestLogger())
	req := pagesRequest(t, "GET", "/api/v1/audit", r.wsID, r.adminID, "ADMIN", "")
	out := httptest.NewRecorder()
	audit.List(out, req)
	if out.Code != 200 || strings.Contains(out.Body.String(), "private-output") {
		t.Errorf("ADMIN audit catalog disclosed private artifact: %d %s", out.Code, out.Body.String())
	}
	// The database also prevents orphaning a private Document; custody must
	// move to a human successor rather than silently become unassigned.
	if _, err := r.db.Exec(`UPDATE pages SET owner_user_id=NULL WHERE id='private-output'`); err == nil {
		t.Fatal("private Document lost its human custodian")
	}
}

func TestPrivateAudienceWorkspaceBackupDoesNotBypassPrivacy(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	seedInstanceUser(t, f.db, "private-backup-author", "private-backup@example.invalid", "ws-old", "MEMBER")
	wmSeedPage(t, f.db, "ws-old", "private-backup-page", "private-backup-page", "Private backup content", "private-backup-author", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='private-backup-page'`)
	for _, path := range []string{"/api/v1/admin/backups", "/api/v1/admin/backups/status", "/api/v1/admin/backups/download"} {
		w := f.do(f.wsAdmin, "GET", path+"?workspace_id=ws-old", "")
		if w.Code != 403 {
			t.Errorf("workspace ADMIN backup bypass: %s returned%d", path, w.Code)
		}
	}
	w := f.do(f.boss, "GET", "/api/v1/admin/backups/status?workspace_id=ws-old", "")
	if w.Code != 200 {
		t.Errorf("instance administrator lost backup access: %d %s", w.Code, w.Body.String())
	}
}
