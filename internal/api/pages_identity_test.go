package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func resolveIdentity(t *testing.T, h *PageHandler, workspace, user, id string) *httptest.ResponseRecorder {
	t.Helper()
	r := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/resolve/id/"+id, workspace, user, "")
	r.SetPathValue("pageId", id)
	w := httptest.NewRecorder()
	h.ResolvePageIdentity(w, r)
	return w
}

func TestPageIdentityRenameReuseAndLiveReach(t *testing.T) {
	h, workspace, owner := pagesAccessFixture(t)
	rec, err := h.loadPage(t.Context(), workspace, "fleet-201")
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range []string{"alice", "carol", "dave", "erin"} {
		w := resolveIdentity(t, h, workspace, reader, rec.ID)
		artifactExpect(t, w, 200)
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 2 || got["page_id"] != rec.ID || got["slug"] != "fleet-201" {
			t.Fatalf("identity=%s error=%v", w.Body.String(), err)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("identity is cacheable")
		}
	}
	// The helper supplies an old OWNER context; live role must prevent a new
	// unrelated MEMBER from using it to enumerate this private Page.
	pagesSeedUser(t, h, workspace, "identity-hidden", "identity-hidden@example.test", "MEMBER")
	hidden := resolveIdentity(t, h, workspace, "identity-hidden", rec.ID)
	missing := resolveIdentity(t, h, workspace, "identity-hidden", "absent-identity")
	artifactExpect(t, hidden, 404)
	artifactExpect(t, missing, 404)
	if hidden.Body.String() != missing.Body.String() || strings.Contains(hidden.Body.String(), rec.ID) {
		t.Fatal("hidden identity differs from missing")
	}
	execOrFatal(t, h.db, `UPDATE pages SET slug='renamed-fleet' WHERE id=?`, rec.ID)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('reused-identity',?,'fleet-201','Different Page',?,'{}')`, workspace, owner)
	current := resolveIdentity(t, h, workspace, "alice", rec.ID)
	artifactExpect(t, current, 200)
	var identity pageIdentityWire
	if err := json.Unmarshal(current.Body.Bytes(), &identity); err != nil || identity.PageID != rec.ID || identity.Slug != "renamed-fleet" {
		t.Fatalf("rename retargeted: %s %v", current.Body.String(), err)
	}
	execOrFatal(t, h.db, `DELETE FROM crew_members WHERE user_id='carol'`)
	artifactExpect(t, resolveIdentity(t, h, workspace, "carol", rec.ID), 404)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='dave'`, rec.ID)
	artifactExpect(t, resolveIdentity(t, h, workspace, "dave", rec.ID), 404)
	otherWorkspace := "identity-other-workspace"
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES(?,'Other','identity-other')`, otherWorkspace)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('identity-other-membership',?,?,'OWNER')`, otherWorkspace, owner)
	cross := resolveIdentity(t, h, otherWorkspace, owner, rec.ID)
	artifactExpect(t, cross, 404)
	if cross.Body.String() != missing.Body.String() {
		t.Fatal("cross-workspace identity disclosed metadata")
	}

	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','dave','read',?)`, rec.ID, owner)
	artifactExpect(t, resolveIdentity(t, h, workspace, "dave", rec.ID), 200)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner)
	artifactExpect(t, resolveIdentity(t, h, workspace, "dave", rec.ID), 404)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, rec.ID)
	deleted := resolveIdentity(t, h, workspace, "alice", rec.ID)
	artifactExpect(t, deleted, 404)
	if deleted.Body.String() != missing.Body.String() {
		t.Fatal("deleted identity differs from missing")
	}
}

func TestPageIdentityUsesLeafFolderACLWithoutExposingHiddenAncestors(t *testing.T) {
	f := newFoldersFixture(t)
	parent := f.createFolder(t, "identity-private-parent", "Hidden ancestor canary", "crew/lookout")
	leaf := f.createFolder(t, "identity-visible-leaf", "Leaf", "crew/engine")
	execOrFatal(t, f.h.db, `UPDATE page_folders SET parent_id=? WHERE id=?`, parent["id"], leaf["id"])
	f.file(t, "identity-visible-leaf", "fleet-201")
	f.share(t, "identity-visible-leaf", "user", "frank", false)
	rec, err := f.h.loadPage(t.Context(), f.wsID, "fleet-201")
	if err != nil {
		t.Fatal(err)
	}
	w := resolveIdentity(t, f.h, f.wsID, "frank", rec.ID)
	artifactExpect(t, w, 200)
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result) != 2 {
		t.Fatalf("identity leaked extra fields: %s %v", w.Body.String(), err)
	}
	for _, hidden := range []string{"Hidden ancestor canary", "identity-private-parent", parent["id"].(string), leaf["id"].(string)} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatalf("identity exposed folder metadata: %s", w.Body.String())
		}
	}
	f.unshare(t, "identity-visible-leaf", "user", "frank")
	artifactExpect(t, resolveIdentity(t, f.h, f.wsID, "frank", rec.ID), 404)
	f.share(t, "identity-private-parent", "user", "frank", false)
	artifactExpect(t, resolveIdentity(t, f.h, f.wsID, "frank", rec.ID), 404)
}

func TestPageIdentityRouterCredentialCeilingsAndExistingSlugRoutes(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	for _, slug := range []string{"resolve", "by-id"} {
		execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Existing Page',?,'{}')`, "identity-"+slug, workspace, slug, user)
	}
	const secret = "synthetic-identity-router-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, err := validator.IssueAccessToken(user, session.ID, "Synthetic", "synthetic@example.test")
	if err != nil {
		t.Fatal(err)
	}
	// Construct the real mux: ambiguous by-id/{id} would panic here against
	// the existing {slug}/grants route, before any request could reach ACL.
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	call := func(path, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path+"?workspace_id="+workspace, nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	prefix := "/api/v1/pages/resolve/id/identity-resolve"
	artifactExpect(t, call(prefix, sessionToken), 200)
	for _, slug := range []string{"resolve", "by-id"} {
		artifactExpect(t, call("/api/v1/pages/"+slug, sessionToken), 200)
	}
	for _, test := range []struct {
		name   string
		scopes any
		want   int
	}{
		{"read", `["pages:read"]`, 200}, {"write-only", `["pages:write"]`, 403},
		{"wildcard", `["pages:*"]`, 200}, {"unrelated", `["agents:read"]`, 403},
		{"legacy-unrestricted", nil, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := "crewship_cli_identity_" + test.name
			execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,?,'synthetic identity',?,'STANDARD',?,'2026-10-15T00:00:00Z')`, test.name, user, hashStandard(token), test.scopes)
			artifactExpect(t, call(prefix, token), test.want)
			execOrFatal(t, db, `UPDATE cli_tokens SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, test.name)
			artifactExpect(t, call(prefix, token), http.StatusUnauthorized)
		})
	}
	execOrFatal(t, db, `UPDATE user_sessions SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, session.ID)
	artifactExpect(t, call(prefix, sessionToken), http.StatusUnauthorized)
}
