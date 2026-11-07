package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// `crewship page grant <slug> --everyone` is PUT …/grants with
// subject_type=workspace. These tests measure what that grant changes for each
// kind of caller on each artifact family: the shared version becomes readable
// to current human members, an unshared later version never does, a guest, a
// non-member and an agent gain nothing, and removing the grant undoes it.

func everyoneGrantCall(p *PageHandler, owner context.Context, method, level string) *httptest.ResponseRecorder {
	body, query := "", ""
	if method == http.MethodPut {
		body = fmt.Sprintf(`{"subject_type":"workspace","level":%q}`, level)
	} else {
		query = "?subject_type=workspace"
	}
	r := httptest.NewRequest(method, "/api/v1/pages/native-page/grants"+query, strings.NewReader(body)).WithContext(context.WithValue(owner, ctxRole, "MEMBER"))
	r.SetPathValue("slug", "native-page")
	w := httptest.NewRecorder()
	if method == http.MethodPut {
		p.PutGrant(w, r)
	} else {
		p.DeleteGrant(w, r)
	}
	return w
}

type everyoneNativePeople struct{ member, nonMember, guest, agent context.Context }

func everyoneNativeCast(t *testing.T, s craftNativeSharedOnly) everyoneNativePeople {
	t.Helper()
	member := craftNativeVersionsMember(t, s.f, "everyone-member", "Member", "MEMBER", "")
	nonMember := craftNativeVersionsMember(t, s.f, "everyone-outsider", "Outsider", "", "")
	guest := craftNativeVersionsMember(t, s.f, "everyone-guest", "Guest", "MEMBER", "")
	mustExec(t, s.f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='everyone-guest'`)
	// An agent run acting on the member's own credential is still not "everyone".
	agent := context.WithValue(member, artifactRunKey{}, &artifactRunAuthority{RunID: "everyone-run", AgentID: "everyone-agent", WorkspaceID: s.f.wsID, HumanUserID: "everyone-member"})
	return everyoneNativePeople{member, nonMember, guest, agent}
}

// everyoneNativeRefused asserts this caller reaches nothing of the document.
func everyoneNativeRefused(t *testing.T, s craftNativeSharedOnly, stage, who string, ctx context.Context) {
	t.Helper()
	if w := craftNativePreviewCall(s.d, ctx); w.Code != 403 {
		t.Fatalf("%s: %s reached the preview: %d %s", stage, who, w.Code, w.Body)
	}
	if w := craftNativeSessionCall(s.h, ctx); w.Code != 403 {
		t.Fatalf("%s: %s opened a session: %d %s", stage, who, w.Code, w.Body)
	}
}

func everyoneNativeSeesPin(t *testing.T, s craftNativeSharedOnly, stage string, ctx context.Context) {
	t.Helper()
	w := craftNativePreviewCall(s.d, ctx)
	var shared craftNativePreviewResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &shared) != nil || shared.Version != "2" || string(shared.Bytes) != craftNativeSharedContent {
		t.Fatalf("%s: preview must be the shared version: %d %s", stage, w.Code, w.Body)
	}
	assertCraftNativeNoPrivateContent(t, stage, w.Body.String())
}

func TestEveryoneGrantOnNativeOpensOnlyTheSharedVersionToMembers(t *testing.T) {
	for _, level := range []string{"read", "comment"} {
		t.Run(level, func(t *testing.T) {
			s := craftNativeSharedOnlyFixture(t)
			c := everyoneNativeCast(t, s)
			for who, ctx := range map[string]context.Context{"member": c.member, "non-member": c.nonMember, "guest": c.guest, "agent": c.agent} {
				everyoneNativeRefused(t, s, "before", who, ctx)
			}

			if w := everyoneGrantCall(s.h.pages, s.owner, http.MethodPut, level); w.Code != 200 {
				t.Fatalf("grant: %d %s", w.Code, w.Body)
			}
			everyoneNativeSeesPin(t, s, "after the grant", c.member)
			for who, ctx := range map[string]context.Context{"non-member": c.nonMember, "guest": c.guest, "agent": c.agent} {
				everyoneNativeRefused(t, s, "after the grant", who, ctx)
			}
			// The later version 3 stays out of reach on every route. Comment opens no
			// session at all; Read opens one that only ever reports the lease.
			var observer string
			session := craftNativeSessionCall(s.h, c.member)
			if level == "comment" {
				if session.Code != 403 {
					t.Fatalf("comment session: %d %s", session.Code, session.Body)
				}
			} else {
				observer, _ = craftNativeIssued(t, s.h, c.member)
				for _, path := range craftNativePrivatePaths(s.d) {
					got := path.call(t, c.member, observer)
					if got.Code != 403 {
						t.Fatalf("%s after the grant: %d %s", path.name, got.Code, got.Body)
					}
					assertCraftNativeNoPrivateContent(t, path.name, got.Body.String())
				}
			}
			// The grant is resolved at read time, so someone who joins later has it.
			joiner := craftNativeVersionsMember(t, s.f, "everyone-joiner", "Joiner", "MEMBER", "")
			everyoneNativeSeesPin(t, s, "a later member", joiner)

			if w := everyoneGrantCall(s.h.pages, s.owner, http.MethodDelete, ""); w.Code != 200 {
				t.Fatalf("revoke: %d %s", w.Code, w.Body)
			}
			for who, ctx := range map[string]context.Context{"member": c.member, "joiner": joiner, "non-member": c.nonMember, "guest": c.guest, "agent": c.agent} {
				everyoneNativeRefused(t, s, "after removal", who, ctx)
			}
			if observer != "" {
				if got := craftNativeLeaseCall(s.h, c.member, observer, "native-page", "read", `{}`); got.Code != 403 {
					t.Fatalf("session outlived the removal: %d %s", got.Code, got.Body)
				}
			}
		})
	}
}

// Edit for everyone gives members the latest version, as any Edit does, but not
// the choice of what readers see: that stays with the owner (PRD §5).
func TestEveryoneEditReachesLatestButNeverSharing(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	craftNativeFreeLease(t, s)
	c := everyoneNativeCast(t, s)
	if w := everyoneGrantCall(s.h.pages, s.owner, http.MethodPut, "write"); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	token, share := craftNativeHolder(t, s, c.member)
	read := craftNativeDocumentCall(t, s.d, c.member, token, "read", nil)
	var document struct {
		Version string `json:"version"`
	}
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &document) != nil || document.Version != "3" {
		t.Fatalf("Edit reads latest: %d %s", read.Code, read.Body)
	}
	if w := craftNativeShareCall(t, s.d, c.member, token, share); w.Code != 403 {
		t.Fatalf("Edit shared: %d %s", w.Code, w.Body)
	}
	everyoneNativeSeesPin(t, s, "after the refused share", c.member)
	for who, ctx := range map[string]context.Context{"non-member": c.nonMember, "guest": c.guest, "agent": c.agent} {
		everyoneNativeRefused(t, s, "edit for everyone", who, ctx)
	}
}

// Current behaviour, pinned on purpose: a private document (agent output, a
// Sheet) has no route to readers. The grant is refused, a folder does not reach
// it, and the owner's version share moves the pin but keeps the private audience,
// unlike a Project. Lifting that is the owner's decision.
func TestEveryoneGrantIsRefusedOnPrivateNative(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	craftNativeFreeLease(t, s)
	c := everyoneNativeCast(t, s)
	mustExec(t, s.f.db, `UPDATE pages SET private_audience=1 WHERE id='native-page'`)
	mustExec(t, s.f.db, `INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('private-crew',?,'Private','private','free')`, s.f.wsID)
	mustExec(t, s.f.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('private-folder',?,'private-folder','Folder','private-crew',datetime('now'),datetime('now'))`, s.f.wsID)
	mustExec(t, s.f.db, `UPDATE pages SET folder_id='private-folder' WHERE id='native-page'`)
	mustExec(t, s.f.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('private-folder','workspace','',1,0,datetime('now'))`)
	token, share := craftNativeHolder(t, s, s.owner)
	if w := everyoneGrantCall(s.h.pages, s.owner, http.MethodPut, "read"); w.Code != 409 || !strings.Contains(w.Body.String(), "private artifact") {
		t.Fatalf("grant on a private document: %d %s", w.Code, w.Body)
	}
	if w := craftNativeShareCall(t, s.d, s.owner, token, share); w.Code != 200 {
		t.Fatalf("owner share: %d %s", w.Code, w.Body)
	}
	var private bool
	if err := s.f.db.QueryRow(`SELECT private_audience FROM pages WHERE id='native-page'`).Scan(&private); err != nil || !private {
		t.Fatalf("audience after share: %v %v", private, err)
	}
	var rows int
	if err := s.f.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id='native-page'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("grant rows %d: %v", rows, err)
	}
	var pin int
	if err := s.f.db.QueryRow(`SELECT shared_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&pin); err != nil || pin != 3 {
		t.Fatalf("the owner's share still moves the pin: %d %v", pin, err)
	}
	// Neither the folder shared with everyone nor the version share opens it.
	everyoneNativeRefused(t, s, "private", "member", c.member)
}

// An agent is named or it has nothing. A workspace grant gives it no row, and a
// grant naming it, the control, does.
func TestEveryoneGrantNeverReachesAnAgent(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	mustExec(t, s.f.db, `INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('everyone-crew',?,'Crew','everyone','free')`, s.f.wsID)
	mustExec(t, s.f.db, `INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('everyone-agent','everyone-crew',?,'Agent','everyone-agent')`, s.f.wsID)
	if w := everyoneGrantCall(s.h.pages, s.owner, http.MethodPut, "write"); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	ctx := context.WithValue(s.owner, pageAuthReaderKey{}, nil)
	rec, err := s.h.pages.loadPageForRead(s.owner, s.f.wsID, "@native-page")
	if err != nil {
		t.Fatal(err)
	}
	if grants, err := s.h.pages.agentGrants(ctx, s.f.wsID, rec, "everyone-agent"); err != nil || len(grants) != 0 {
		t.Fatalf("workspace grant reached an agent: %v %v", grants, err)
	}
	viewer, err := s.h.pages.agentViewer(ctx, s.f.wsID, "everyone-agent")
	if err != nil {
		t.Fatal(err)
	}
	if seen, err := s.h.pages.canSeePage(ctx, s.f.wsID, rec, nil, viewer); err != nil || seen {
		t.Fatalf("agent sees the page through everyone: %v %v", seen, err)
	}
	mustExec(t, s.f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','agent','everyone-agent','read',?)`, s.f.userID)
	if grants, err := s.h.pages.agentGrants(ctx, s.f.wsID, rec, "everyone-agent"); err != nil || len(grants) != 1 {
		t.Fatalf("control: a named agent grant is live: %v %v", grants, err)
	}
}

// The same measurement on a shared Project through the production Router: pin
// v2, latest v3. Grants here go through the real route, as the CLI does.
func TestEveryoneGrantOnProjectOpensOnlyTheSharedVersionToMembers(t *testing.T) {
	m := newRBACMatrix(t, true)
	seedInstanceUser(t, m.f.db, "pureguest", "pureguest@ex.com", rbacWS, "MEMBER")
	m.tokens["pureguest"] = mintTokenFor(t, m.f.db, "pureguest", "rbacmatrixpureguest000000000")
	mustExec(t, m.f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='pureguest'`)
	seedInstanceUser(t, m.f.db, "outsider", "outsider@ex.com", "", "")
	m.tokens["outsider"] = mintTokenFor(t, m.f.db, "outsider", "rbacmatrixoutsider00000000000")
	agent := func(path string) int {
		r := httptest.NewRequest(http.MethodGet, path+"?workspace_id="+rbacWS, nil)
		r.Header.Set("Authorization", "Bearer "+m.tokens["member"])
		r.Header.Set(actingAgentSlugHeader, "someone")
		w := httptest.NewRecorder()
		m.f.r.ServeHTTP(w, r)
		if rbacMarkers(w.Body.String()) != "" {
			t.Fatalf("agent received a version: %s", w.Body)
		}
		return w.Code
	}
	const page = "/api/v1/pages/matrix"
	reads := []string{page + "/artifact", page + "/artifact?version=2", page + "/artifact?version=3", page + "/artifact?version=1", page + "/artifact/versions"}
	nothing := func(stage string, users ...string) {
		t.Helper()
		for _, user := range users {
			for _, path := range reads {
				got := m.do(user, http.MethodGet, path, "")
				if got.Code/100 == 2 || rbacMarkers(got.Body.String()) != "" {
					t.Fatalf("%s: %s reached %s: %d %.200s", stage, user, path, got.Code, got.Body)
				}
			}
		}
	}
	nothing("before", "member", "pureguest", "outsider")
	if code := agent(page + "/artifact"); code/100 == 2 {
		t.Fatalf("before: agent read %d", code)
	}
	if w := m.do("author", http.MethodPut, page+"/grants", `{"subject_type":"workspace","level":"read"}`); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	for path, want := range map[string]string{page + "/artifact": "2", page + "/artifact?version=2": "2", page + "/artifact/versions": "2"} {
		if got := m.do("member", http.MethodGet, path, ""); got.Code != 200 || rbacMarkers(got.Body.String()) != want {
			t.Fatalf("member %s: %d markers %q", path, got.Code, rbacMarkers(got.Body.String()))
		}
	}
	for _, path := range []string{page + "/artifact?version=3", page + "/artifact?version=1"} {
		if got := m.do("member", http.MethodGet, path, ""); got.Code != 404 || rbacMarkers(got.Body.String()) != "" {
			t.Fatalf("member reached an unshared version %s: %d %.200s", path, got.Code, got.Body)
		}
	}
	nothing("after the grant", "pureguest", "outsider")
	if code := agent(page + "/artifact"); code/100 == 2 {
		t.Fatalf("after the grant: agent read %d", code)
	}
	if w := m.do("author", http.MethodDelete, page+"/grants?subject_type=workspace", ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	nothing("after removal", "member", "pureguest", "outsider")
}

// Design: pin v2, latest v3, as the tests above.
func TestEveryoneGrantOnDesignOpensOnlyTheSharedVersionToMembers(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	f.addUser(t, "design-member")
	f.addUser(t, "design-guest")
	mustExec(t, f.h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='design-guest'`)
	mustExec(t, f.h.db, `INSERT INTO users(id,email,full_name) VALUES('design-outsider','design-outsider@example.test','Outsider')`)
	for _, step := range []struct{ op, body string }{
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Shared title"}}]}`},
		{"share", `{"version":2}`},
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"Unshared title"}}]}`},
	} {
		if w := f.call(t, f.owner, step.op, step.body); w.Code != 200 {
			t.Fatalf("setup %s %d %s", step.op, w.Code, w.Body)
		}
	}
	grant := func(method, query, body string) *httptest.ResponseRecorder {
		t.Helper()
		router, err := NewRouter(f.h.db, "synthetic-design-everyone-secret-long-enough", newTestLogger(), WithInternalToken("synthetic-design-everyone-master-long-enough"))
		if err != nil {
			t.Fatal(err)
		}
		defer router.Shutdown()
		r := httptest.NewRequest(method, "/api/v1/pages/board/grants"+query, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.token(t, f.owner))
		r.Header.Set("X-Workspace-ID", f.workspace)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	refused := func(stage string, users ...string) {
		t.Helper()
		for _, user := range users {
			w := f.call(t, user, "get", "")
			if w.Code/100 == 2 || strings.Contains(w.Body.String(), "title") {
				t.Fatalf("%s: %s reached the Design: %d %s", stage, user, w.Code, w.Body)
			}
		}
	}
	people := []string{"design-member", "design-guest", "design-outsider"}
	refused("before", people...)
	if w := grant(http.MethodPut, "", `{"subject_type":"workspace","level":"read"}`); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	got := f.call(t, "design-member", "get", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), "Shared title") || strings.Contains(got.Body.String(), "Unshared title") || !strings.Contains(got.Body.String(), `"can_edit":false`) {
		t.Fatalf("member view: %d %s", got.Code, got.Body)
	}
	old := httptest.NewRecorder()
	f.h.GetDesign(old, f.request(t, "design-member", http.MethodGet, "?version=3", ""))
	if old.Code != 404 || strings.Contains(old.Body.String(), "Unshared title") {
		t.Fatalf("member selected an unshared version: %d %s", old.Code, old.Body)
	}
	refused("after the grant", "design-guest", "design-outsider")
	if w := grant(http.MethodDelete, "?subject_type=workspace", ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	refused("after removal", people...)
}

// Design's share rule, measured: Edit and the workspace roles may change the
// canvas but not the shared pin.
func TestDesignShareIsRefusedToEveryoneButThePageOwner(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	for _, tc := range []struct{ user, role string }{{"design-edit", "MEMBER"}, {"design-admin", "ADMIN"}, {"design-wsowner", "OWNER"}} {
		f.addUser(t, tc.user)
		mustExec(t, f.h.db, `UPDATE workspace_members SET role=? WHERE user_id=?`, tc.role, tc.user)
	}
	if w := f.grant(t, "design-edit", "write"); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Saved"}}]}`); w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	pin := func() (n int) {
		t.Helper()
		if err := f.h.db.QueryRow(`SELECT shared_version FROM artifact_version_heads WHERE page_id=?`, f.page).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	canShare := func(user string) bool {
		t.Helper()
		got := f.call(t, user, "get", "")
		var view struct {
			CanEdit  bool  `json:"can_edit"`
			CanShare *bool `json:"can_share"`
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &view) != nil || !view.CanEdit || view.CanShare == nil {
			t.Fatalf("%s snapshot must carry can_edit and can_share: %d %s", user, got.Code, got.Body)
		}
		return *view.CanShare
	}
	if !canShare(f.owner) {
		t.Fatal("the owner's snapshot must offer share")
	}
	for _, user := range []string{"design-edit", "design-admin", "design-wsowner"} {
		if canShare(user) {
			t.Errorf("%s snapshot offers share", user)
		}
		if w := f.call(t, user, "share", `{"version":2}`); w.Code != 403 {
			t.Errorf("%s shared a Design version: %d %s", user, w.Code, w.Body)
		}
		if pin() != 0 {
			t.Fatalf("%s moved the pin", user)
		}
	}
	if w := f.call(t, f.owner, "share", `{"version":2}`); w.Code != 200 || pin() != 2 {
		t.Fatalf("owner share: %d %s", w.Code, w.Body)
	}
}

// A real agent run, on real run and crew tokens. Its human can read the Project
// through the workspace grant, and the run still cannot: the page is outside its
// mandate, which answers exactly as a page that does not exist.
func TestEveryoneGrantGivesAnAgentRunNothing(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	const master = "synthetic-artifact-everyone-scope-master"
	router, err := NewRouter(f.rig.db, "synthetic-artifact-everyone-scope-secret", newTestLogger(), WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-everyone-scope-human")
	human := func(method, path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		req := pagesRequest(t, method, path, f.a.WorkspaceID, f.a.HumanUserID, "MEMBER", body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", f.a.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
		req.SetPathValue("slug", "everyone-project")
		got := httptest.NewRecorder()
		handler(got, req)
		return got
	}
	body := `{"slug":"everyone-project","name":"Everyone",` + strings.TrimPrefix(artifactProjectBody(0, "SHARED-MARKER"), "{")
	if got := human(http.MethodPost, "/api/v1/pages/artifacts", body, router.pages.CreateArtifactProject); got.Code != 201 {
		t.Fatalf("create: %d %s", got.Code, got.Body)
	}
	if got := human(http.MethodPost, "/api/v1/pages/everyone-project/artifact/share", `{"base_version":1,"version":1}`, router.pages.ShareArtifactProject); got.Code != 200 {
		t.Fatalf("share: %d %s", got.Code, got.Body)
	}
	// The run holds a real mandate on another Project, so a refusal below is about
	// this page and not about a dead run.
	own := `{"slug":"everyone-own","name":"Own",` + strings.TrimPrefix(artifactProjectBody(0, "OWN-MARKER"), "{")
	if got := human(http.MethodPost, "/api/v1/pages/artifacts", own, router.pages.CreateArtifactProject); got.Code != 201 {
		t.Fatalf("create own: %d %s", got.Code, got.Body)
	}
	var ownID string
	if err = f.rig.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='everyone-own'`, f.a.WorkspaceID).Scan(&ownID); err != nil {
		t.Fatal(err)
	}
	consent, _ := json.Marshal(map[string]any{"run_id": f.a.RunID, "page_id": ownID})
	if got := human(http.MethodPost, "/api/v1/pages/artifacts/consent", string(consent), router.pages.GrantArtifactProjectRunConsent); got.Code != 200 {
		t.Fatalf("consent: %d %s", got.Code, got.Body)
	}
	// The Project now belongs to someone else; the run's human is a plain member.
	mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES('everyone-project-owner','everyone-project-owner@example.test')`)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('everyone-project-m',?,'everyone-project-owner','MEMBER')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.a.HumanUserID)
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id='everyone-project-owner' WHERE slug='everyone-project'`)
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT id,'workspace','','read','everyone-project-owner' FROM pages WHERE slug='everyone-project'`)
	if got := human(http.MethodGet, "/api/v1/pages/everyone-project/artifact", "", router.pages.GetArtifactProject); got.Code != 200 || !strings.Contains(got.Body.String(), "SHARED-MARKER") {
		t.Fatalf("calibration: the run's human reads through the grant: %d %s", got.Code, got.Body)
	}
	key := internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID)
	runToken := internaltoken.DeriveAgentRunToken(key, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	crewToken := internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID)
	tool := func(slug string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"operation": "artifact_versions", "slug": slug, "payload": json.RawMessage(`{}`)})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/artifact-tool", bytes.NewReader(raw))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+runToken)
		req.Header.Set("X-Internal-Token", crewToken)
		req.Header.Set("Content-Type", "application/json")
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		return got
	}
	if got := tool("everyone-own"); got.Code != 200 {
		t.Fatalf("calibration: a run reads the page in its mandate: %d %s", got.Code, got.Body)
	}
	absent := tool("absent-everyone-project")
	if got := tool("everyone-project"); got.Code != 404 || got.Body.String() != absent.Body.String() || strings.Contains(got.Body.String(), "SHARED-MARKER") {
		t.Fatalf("agent run through the workspace grant: %d %s (absent: %d %s)", got.Code, got.Body, absent.Code, absent.Body)
	}
}
