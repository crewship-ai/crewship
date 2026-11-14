package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5317 PR3 (D2): "agents of the owning crew" as a folder subject.

// folder files page in a new folder owned by crew, with the given agent access.
func (g *runScopeRig) folder(t *testing.T, slug, crew, access string, pages ...string) string {
	t.Helper()
	id := "folder-" + slug
	mustExec(t, g.f.rig.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,agent_access) VALUES(?,?,?,?,?,'2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',?)`,
		id, g.f.a.WorkspaceID, slug, slug, crew, access)
	for _, p := range pages {
		mustExec(t, g.f.rig.db, `UPDATE pages SET folder_id=? WHERE id=?`, id, p)
	}
	return id
}

// shared creates an artifact owned by someone else that the human reads.
func (g *runScopeRig) shared(t *testing.T, slug string) string {
	t.Helper()
	id := g.create(t, g.f.rig.adminID, "OWNER", slug)
	g.grant(t, id, "user", g.f.a.HumanUserID, "read")
	return id
}

func TestFolderAgents_ReachTheOwningCrewsAgents(t *testing.T) {
	for _, tc := range []struct {
		access string
		reach  bool
	}{{"none", false}, {"read", true}, {"propose", true}} {
		t.Run(tc.access, func(t *testing.T) {
			g := newRunScopeRig(t)
			page := g.shared(t, "folder-reach")
			g.folder(t, "reach", g.f.a.CrewID, tc.access, page)
			g.start(t)
			got := g.tool("artifact_versions", "folder-reach", `{}`)
			missing := g.tool("artifact_versions", "folder-nowhere", `{}`)
			if tc.reach && got.Code != 200 {
				t.Fatalf("folder %s did not reach: %d %s", tc.access, got.Code, got.Body)
			}
			if !tc.reach && (got.Code != missing.Code || got.Body.String() != missing.Body.String()) {
				t.Fatalf("folder %s: %d %s, missing: %d %s", tc.access, got.Code, got.Body, missing.Code, missing.Body)
			}
		})
	}
}

func TestFolderAgents_StillIntersectedWithTheHuman(t *testing.T) {
	g := newRunScopeRig(t)
	page := g.create(t, g.f.rig.adminID, "OWNER", "folder-human-blind")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
	g.folder(t, "blind", g.f.a.CrewID, "read", page)
	g.start(t)
	if w := g.tool("artifact_versions", "folder-human-blind", `{}`); w.Code == 200 {
		t.Fatal("the folder reached a page its human cannot read")
	}
}

func TestFolderAgents_NeverAnotherCrewsAgents(t *testing.T) {
	g := newRunScopeRig(t)
	other := seedCrewRow(t, g.f.rig.db, "crew-folder-other", g.f.a.WorkspaceID, "Other", "folder-other")
	page := g.shared(t, "folder-other-crew")
	g.folder(t, "other", other, "read", page)
	g.start(t)
	if w := g.tool("artifact_versions", "folder-other-crew", `{}`); w.Code == 200 {
		t.Fatal("a folder of another crew reached this crew's agent")
	}
}

func TestFolderAgents_NeverPrivate(t *testing.T) {
	g := newRunScopeRig(t)
	page := g.create(t, g.f.a.HumanUserID, "MEMBER", "folder-private")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
	g.folder(t, "private", g.f.a.CrewID, "read", page)
	g.start(t)
	if w := g.tool("artifact_versions", "folder-private", `{}`); w.Code == 200 {
		t.Fatal("a folder reached a private artifact")
	}
}

// A folder never gives write, in the schema and in the run, even on an
// artifact the human owns.
func TestFolderAgents_NeverWrite(t *testing.T) {
	g := newRunScopeRig(t)
	if _, err := g.f.rig.db.Exec(`INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,agent_access) VALUES('folder-w',?,'w','w',?,'x','x','write')`, g.f.a.WorkspaceID, g.f.a.CrewID); err == nil {
		t.Fatal("the schema stored a write level for a folder's agents")
	}
	own := g.create(t, g.f.a.HumanUserID, "MEMBER", "folder-own")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, own)
	g.folder(t, "own", g.f.a.CrewID, "propose", own)
	g.start(t)
	if w := g.tool("artifact_versions", "folder-own", `{}`); w.Code != 200 {
		t.Fatalf("baseline read: %d %s", w.Code, w.Body)
	}
	if w := g.tool("artifact_publish", "folder-own", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("a run wrote through a folder")
	}
}

// #5317 required test: a run without a human cannot write (or read) through a
// folder. An unattended run has no principal and the tool refuses it.
func TestFolderAgents_RunWithoutAHumanGetsNothing(t *testing.T) {
	g := newRunScopeRig(t)
	own := g.create(t, g.f.a.HumanUserID, "MEMBER", "folder-unattended")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, own)
	g.folder(t, "unattended", g.f.a.CrewID, "propose", own)
	// No start: the run has no human principal (a routine or webhook run).
	for _, op := range []string{"artifact_versions", "artifact_publish"} {
		payload := `{}`
		if op == "artifact_publish" {
			payload = artifactProjectBody(1, "Second")
		}
		if w := g.tool(op, "folder-unattended", payload); w.Code == 200 {
			t.Fatalf("%s succeeded for a run without a human", op)
		}
	}
}

func TestFolderAgents_FlagOffReachesNothing(t *testing.T) {
	g := newRunScopeRig(t)
	page := g.shared(t, "folder-flag-off")
	g.folder(t, "flag-off", g.f.a.CrewID, "read", page)
	g.start(t)
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if w := g.tool("artifact_versions", "folder-flag-off", `{}`); w.Code == 200 {
		t.Fatal("flag off: a folder reached an agent")
	}
}

// The read cap counts folder reads like any standing read.
func TestFolderAgents_ReadsCountTowardsTheRunCap(t *testing.T) {
	g := newRunScopeRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=2 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	var ids []string
	for i := 1; i <= 3; i++ {
		ids = append(ids, g.shared(t, fmt.Sprintf("folder-cap-%d", i)))
	}
	g.folder(t, "cap", g.f.a.CrewID, "read", ids...)
	g.start(t)
	for i := 1; i <= 2; i++ {
		if w := g.tool("artifact_versions", fmt.Sprintf("folder-cap-%d", i), `{}`); w.Code != 200 {
			t.Fatalf("read %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := g.tool("artifact_versions", "folder-cap-3", `{}`); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "artifact_run_limit") {
		t.Fatalf("third distinct folder read: %d %s", w.Code, w.Body)
	}
}

func folderAgentAccessFlag(t *testing.T, f *foldersFixture) {
	t.Helper()
	mustExec(t, f.h.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('folder-agents-flag','ff_artifact_agent_access_modes',?,1)`, f.wsID)
}

func folderAgentAccessOf(t *testing.T, f *foldersFixture, slug string) (string, int64) {
	t.Helper()
	var level string
	var version int64
	if err := f.h.db.QueryRow(`SELECT agent_access,acl_version FROM page_folders WHERE workspace_id=? AND slug=?`, f.wsID, slug).Scan(&level, &version); err != nil {
		t.Fatal(err)
	}
	return level, version
}

// D2: set only by an admin or the owning crew's MANAGER+, at most read or
// propose, fenced on acl_version, audited, and only with the flag on.
func TestFolderAgentAccess_WhoMaySetItAndToWhat(t *testing.T) {
	f := newFoldersFixture(t)
	folderAgentAccessFlag(t, f)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	put := func(user, role, body string) *httptest.ResponseRecorder {
		return f.call(t, "PUT", "/api/v1/page-folders/engine-ops/agent-access", user, role, body)
	}
	_, v := folderAgentAccessOf(t, f, "engine-ops")
	body := func(level string, version int64) string {
		return fmt.Sprintf(`{"level":%q,"acl_version":%d}`, level, version)
	}
	for _, tc := range []struct {
		name, user, role string
		status           int
	}{
		{"a member of the owning crew", "mel", "MEMBER", http.StatusForbidden},
		{"a MANAGER of no crew", "max", "MANAGER", http.StatusNotFound},
		{"a member who reaches nothing", "frank", "MEMBER", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectStatus(t, put(tc.user, tc.role, body("read", v)), tc.status)
		})
	}
	expectStatus(t, put("mia", "MANAGER", body("write", v)), http.StatusBadRequest)
	expectStatus(t, put("mia", "MANAGER", `{"level":"read"}`), http.StatusBadRequest)
	expectStatus(t, put("mia", "MANAGER", body("read", v+7)), http.StatusConflict)
	if level, now := folderAgentAccessOf(t, f, "engine-ops"); level != "none" || now != v {
		t.Fatalf("refused requests changed the folder: %s %d", level, now)
	}
	rr := put("mia", "MANAGER", body("read", v))
	expectStatus(t, rr, http.StatusOK)
	if doc := decodeFoldersJSON(t, rr); doc["agent_access"] != "read" || doc["acl_version"] != float64(v+1) {
		t.Fatalf("response %v", doc)
	}
	expectStatus(t, put("erin", "ADMIN", body("propose", v+1)), http.StatusOK)
	if level, now := folderAgentAccessOf(t, f, "engine-ops"); level != "propose" || now != v+2 {
		t.Fatalf("after two changes: %s %d", level, now)
	}
	audits := 0
	for _, e := range f.spy.entries {
		if e.Type == entryPageFolderAgentAccessChanged {
			audits++
		}
	}
	if audits != 2 {
		t.Fatalf("audit entries %d, want 2", audits)
	}
	rr = f.call(t, "GET", "/api/v1/page-folders/engine-ops/acl", "mia", "MANAGER", "")
	expectStatus(t, rr, http.StatusOK)
	if doc := decodeFoldersJSON(t, rr); doc["agent_access"] != "propose" {
		t.Fatalf("ACL document %v", doc)
	}
}

func TestFolderAgentAccess_FlagOffRefusesAnythingButNone(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	_, v := folderAgentAccessOf(t, f, "engine-ops")
	rr := f.call(t, "PUT", "/api/v1/page-folders/engine-ops/agent-access", "erin", "ADMIN", fmt.Sprintf(`{"level":"read","acl_version":%d}`, v))
	expectStatus(t, rr, http.StatusConflict, "artifact_agent_access_modes")
	if level, _ := folderAgentAccessOf(t, f, "engine-ops"); level != "none" {
		t.Fatalf("flag off stored %s", level)
	}
	expectStatus(t, f.call(t, "PUT", "/api/v1/page-folders/engine-ops/agent-access", "erin", "ADMIN", fmt.Sprintf(`{"level":"none","acl_version":%d}`, v)), http.StatusOK)
}

// #5317 required test: moving an artifact into a folder (even one its agents
// reach) does not change who owns it.
func TestFolderAgentAccess_MovingIntoAFolderKeepsTheOwner(t *testing.T) {
	f := newFoldersFixture(t)
	folderAgentAccessFlag(t, f)
	f.createFolder(t, "engine-ops", "Engine ops", "crew/engine")
	_, v := folderAgentAccessOf(t, f, "engine-ops")
	expectStatus(t, f.call(t, "PUT", "/api/v1/page-folders/engine-ops/agent-access", "erin", "ADMIN", fmt.Sprintf(`{"level":"read","acl_version":%d}`, v)), http.StatusOK)
	var before string
	if err := f.h.db.QueryRow(`SELECT coalesce(owner_user_id,'')||'/'||coalesce(owner_crew_id,'') FROM pages WHERE workspace_id=? AND slug='fleet-201'`, f.wsID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	move := fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d}`, f.pagesVersion(t, "fleet-201"), f.aclVersion(t, "engine-ops"))
	expectStatus(t, f.call(t, "POST", "/api/v1/page-folders/engine-ops/pages", "erin", "ADMIN", move), http.StatusOK)
	if f.folderOf(t, "fleet-201") == "" {
		t.Fatal("baseline: the page was not filed")
	}
	var after string
	if err := f.h.db.QueryRow(`SELECT coalesce(owner_user_id,'')||'/'||coalesce(owner_crew_id,'') FROM pages WHERE workspace_id=? AND slug='fleet-201'`, f.wsID).Scan(&after); err != nil || after != before {
		t.Fatalf("owner %q -> %q %v", before, after, err)
	}
}

// Review (PR3): folder reach follows the agent's and the crew's live state.
func TestFolderAgents_FollowLiveAgentAndCrew(t *testing.T) {
	cases := []struct {
		name   string
		change func(g *runScopeRig)
	}{
		{"agent moved to another crew", func(g *runScopeRig) {
			other := seedCrewRow(t, g.f.rig.db, "crew-folder-moved", g.f.a.WorkspaceID, "Moved", "folder-moved")
			mustExec(t, g.f.rig.db, `UPDATE agents SET crew_id=? WHERE id=?`, other, g.f.a.AgentID)
		}},
		{"owning crew deleted", func(g *runScopeRig) {
			mustExec(t, g.f.rig.db, `UPDATE crews SET deleted_at='2026-11-14T00:00:00Z' WHERE id=?`, g.f.a.CrewID)
		}},
		{"agent deleted", func(g *runScopeRig) {
			mustExec(t, g.f.rig.db, `UPDATE agents SET deleted_at='2026-11-14T00:00:00Z' WHERE id=?`, g.f.a.AgentID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newRunScopeRig(t)
			page := g.shared(t, "folder-live")
			g.folder(t, "live", g.f.a.CrewID, "read", page)
			g.start(t)
			if w := g.tool("artifact_versions", "folder-live", `{}`); w.Code != 200 {
				t.Fatalf("folder reach before the change: %d %s", w.Code, w.Body)
			}
			tc.change(g)
			if w := g.tool("artifact_versions", "folder-live", `{}`); w.Code == 200 {
				t.Fatalf("folder reach survived: %s", tc.name)
			}
		})
	}
}
