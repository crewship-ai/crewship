package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5317 step 5.3: with the flag on, a chat run reaches a Design by the same
// reach kinds as an artifact project (attached, created, standing, folder and
// the existing exact-run consent), intersected with its human as an ordinary
// member, refused like a missing Design, counted against run_read_limit for
// standing and folder reads, and audited as artifact.agent_run_accessed.

const designRunOp = `{"operations":[{"op":"upsert","node":{"id":"f","type":"frame","name":"Frame","x":0,"y":0,"width":100,"height":100,"fill":"#fff"}}]}`

func newDesignRunRig(t *testing.T) *runScopeRig {
	t.Helper()
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	return newRunScopeRig(t)
}

// design creates a Design through the production human handler.
func (g *runScopeRig) design(t *testing.T, userID, role, slug string) string {
	t.Helper()
	w := g.designHuman(t, g.router.pages.CreateDesign, userID, role, slug, `{"slug":"`+slug+`","name":"`+slug+`"}`)
	if w.Code != 201 {
		t.Fatalf("seed design %s: %d %s", slug, w.Code, w.Body)
	}
	var id string
	if err := g.f.rig.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, g.f.a.WorkspaceID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (g *runScopeRig) designHuman(t *testing.T, handler func(http.ResponseWriter, *http.Request), userID, role, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, ok := g.tokens[userID]
	if !ok {
		token = mintTokenFor(t, g.f.rig.db, userID, "run-scope-"+userID)
		g.tokens[userID] = token
	}
	req := pagesRequest(t, http.MethodPost, "/api/v1/pages/"+slug+"/design", g.f.a.WorkspaceID, userID, role, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
	req.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func (g *runScopeRig) designTool(operation, slug, payload string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]any{"operation": operation, "slug": slug, "payload": json.RawMessage(payload)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/design-tool", bytes.NewReader(raw))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+g.runToken)
	req.Header.Set("X-Internal-Token", g.crewTok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	return w
}

func designVersionOf(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	var got struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("design read body %q: %v", w.Body, err)
	}
	return got.Version
}

// Attached Design readable; an unattached one is refused exactly like a
// Design that does not exist.
func TestRunScopeDesign_AttachedReadableUnattachedLikeMissing(t *testing.T) {
	g := newDesignRunRig(t)
	attached := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-attached")
	g.design(t, g.f.a.HumanUserID, "MEMBER", "design-sibling")
	g.start(t, attached)
	if w := g.designTool("design_get", "design-attached", `{}`); w.Code != 200 {
		t.Fatalf("attached Design not readable by its run: %d %s", w.Code, w.Body)
	}
	if w := g.designTool("design_versions", "@"+attached, `{}`); w.Code != 200 {
		t.Fatalf("attached Design versions not readable by its run: %d %s", w.Code, w.Body)
	}
	sibling := g.designTool("design_get", "design-sibling", `{}`)
	missing := g.designTool("design_get", "design-nowhere", `{}`)
	if sibling.Code == 200 {
		t.Fatalf("an unattached Design was readable: %s", sibling.Body)
	}
	if sibling.Code != missing.Code || sibling.Body.String() != missing.Body.String() {
		t.Fatalf("refusal tells unattached from missing: %d %q vs %d %q", sibling.Code, sibling.Body, missing.Code, missing.Body)
	}
	rows := runAuditRows(t, g)
	if len(rows) != 1 {
		t.Fatalf("want one read record, got %+v", rows)
	}
	g.wantAudit(t, rows[0], attached, "referenced", "read")
}

// Writes: the human's own attached Design is written (and audited once);
// standing and folder reach read but never write, even with the agent's own
// write grant.
func TestRunScopeDesign_WritesOnlyThroughWritableReach(t *testing.T) {
	g := newDesignRunRig(t)
	own := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-own")
	standing := g.design(t, g.f.rig.adminID, "OWNER", "design-standing")
	g.grant(t, standing, "user", g.f.a.HumanUserID, "write")
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, standing, g.f.a.AgentID, g.f.rig.adminID)
	folder := g.design(t, g.f.rig.adminID, "OWNER", "design-folder")
	g.grant(t, folder, "user", g.f.a.HumanUserID, "write")
	g.folder(t, "designs", g.f.a.CrewID, "propose", folder)
	g.start(t, own)

	for i := 0; i < 2; i++ {
		if w := g.designTool("design_apply", "design-own", designRunOp); w.Code != 200 {
			t.Fatalf("write %d to the human's own attached Design: %d %s", i, w.Code, w.Body)
		}
	}
	for _, slug := range []string{"design-standing", "design-folder"} {
		if w := g.designTool("design_get", slug, `{}`); w.Code != 200 {
			t.Fatalf("read %s: %d %s", slug, w.Code, w.Body)
		}
		before := designVersionCount(t, g.f, map[string]string{"design-standing": standing, "design-folder": folder}[slug])
		for _, op := range []string{"design_apply", "design_snapshot", "design_restore"} {
			payload := designRunOp
			if op == "design_snapshot" {
				payload = `{}`
			} else if op == "design_restore" {
				payload = `{"version":1}`
			}
			if w := g.designTool(op, slug, payload); w.Code == 200 {
				t.Fatalf("%s wrote %s through read-only reach: %s", op, slug, w.Body)
			}
		}
		if after := designVersionCount(t, g.f, map[string]string{"design-standing": standing, "design-folder": folder}[slug]); after != before {
			t.Fatalf("a refused write versioned %s: %d -> %d", slug, before, after)
		}
	}
	want := map[string]string{own + "/write": "referenced", standing + "/read": "standing", folder + "/read": "folder"}
	rows := runAuditRows(t, g)
	got := map[string]string{}
	for _, r := range rows {
		g.wantAudit(t, r, r.Page, r.Reach, r.Operation)
		got[r.Page+"/"+r.Operation] = r.Reach
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("audit %s = %q, want %q; rows %+v", k, got[k], v, rows)
		}
	}
	if _, ok := got[standing+"/write"]; ok {
		t.Fatalf("a refused write was audited: %+v", rows)
	}
	if len(rows) != len(want) {
		t.Fatalf("audit rows %+v, want exactly %v", rows, want)
	}
}

// Amendment 7: a Viewer's run reads the shared version, an Editor's run the
// latest.
func TestRunScopeDesign_ReadsTheVersionTheHumanIsEntitledTo(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  int64
	}{{"read", 1}, {"comment", 1}, {"write", 2}} {
		t.Run(tc.level, func(t *testing.T) {
			g := newDesignRunRig(t)
			id := g.design(t, g.f.rig.adminID, "OWNER", "design-versions")
			if w := g.designHuman(t, g.router.pages.ApplyDesign, g.f.rig.adminID, "OWNER", "design-versions", designRunOp); w.Code != 200 {
				t.Fatalf("owner edit: %d %s", w.Code, w.Body)
			}
			g.grant(t, id, "user", g.f.a.HumanUserID, tc.level)
			mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=1 WHERE page_id=?`, id)
			var latest int64
			if err := g.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&latest); err != nil || latest != 2 {
				t.Fatalf("fixture latest version %d %v, want 2", latest, err)
			}
			g.start(t, id)
			w := g.designTool("design_get", "design-versions", `{}`)
			if w.Code != 200 {
				t.Fatalf("read: %d %s", w.Code, w.Body)
			}
			if got := designVersionOf(t, w); got != tc.want {
				t.Fatalf("a %s grantee's run read version %d, want %d", tc.level, got, tc.want)
			}
			if tc.want == 1 {
				if w := g.designTool("design_get", "design-versions", `{"version":2}`); w.Code == 200 {
					t.Fatalf("a %s grantee's run read the unshared latest: %s", tc.level, w.Body)
				}
			}
			if w := g.designTool("design_apply", "design-versions", designRunOp); w.Code == 200 {
				t.Fatal("the run wrote a Design its human does not own")
			}
		})
	}
}

// A human who loses access mid-run stops the run's access too.
func TestRunScopeDesign_HumanLosingAccessStopsTheRun(t *testing.T) {
	g := newDesignRunRig(t)
	id := g.design(t, g.f.rig.adminID, "OWNER", "design-revoked")
	g.grant(t, id, "user", g.f.a.HumanUserID, "read")
	g.start(t, id)
	if w := g.designTool("design_get", "design-revoked", `{}`); w.Code != 200 {
		t.Fatalf("baseline: %d %s", w.Code, w.Body)
	}
	mustExec(t, g.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user'`, id)
	revoked := g.designTool("design_get", "design-revoked", `{}`)
	missing := g.designTool("design_get", "design-nowhere", `{}`)
	if revoked.Code == 200 || revoked.Code != missing.Code || revoked.Body.String() != missing.Body.String() {
		t.Fatalf("after revocation: %d %q, missing %d %q", revoked.Code, revoked.Body, missing.Code, missing.Body)
	}
}

// Role-only reach is never delegated: an ADMIN's run does not read another
// person's private Design the admin sees only through the role.
func TestRunScopeDesign_RoleOnlyReachIsNotDelegated(t *testing.T) {
	g := newDesignRunRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, g.f.a.WorkspaceID, g.f.a.HumanUserID)
	g.f.receipt = artifactRunFreshReceipt(t, g.f)
	id := g.design(t, g.f.rig.adminID, "OWNER", "design-admin-only")
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'read',?)`, id, g.f.a.AgentID, g.f.rig.adminID)
	g.start(t)
	if w := g.designTool("design_get", "design-admin-only", `{}`); w.Code == 200 {
		t.Fatalf("role-only reach was delegated: %s", w.Body)
	}
	if rows := runAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("a refusal was audited: %+v", rows)
	}
}

// The existing exact-run consent keeps its Design in reach with the flag on,
// audited as consented; standing reads count against run_read_limit.
func TestRunScopeDesign_ConsentKeepsItsPageAndStandingReadsAreCapped(t *testing.T) {
	g := newDesignRunRig(t)
	consented := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-consented")
	first := g.design(t, g.f.rig.adminID, "OWNER", "design-cap-1")
	second := g.design(t, g.f.rig.adminID, "OWNER", "design-cap-2")
	for _, id := range []string{first, second} {
		g.grant(t, id, "user", g.f.a.HumanUserID, "read")
		mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'read',?)`, id, g.f.a.AgentID, g.f.rig.adminID)
	}
	g.start(t)
	mustExec(t, g.f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at,? FROM artifact_run_principals WHERE run_id=?`, consented, g.f.a.RunID)
	if w := g.designTool("design_apply", "design-consented", designRunOp); w.Code != 200 {
		t.Fatalf("consented write: %d %s", w.Code, w.Body)
	}
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=1, version=version+1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	if w := g.designTool("design_get", "design-cap-1", `{}`); w.Code != 200 {
		t.Fatalf("first standing read: %d %s", w.Code, w.Body)
	}
	if w := g.designTool("design_get", "design-cap-1", `{}`); w.Code != 200 {
		t.Fatalf("re-reading must not count again: %d %s", w.Code, w.Body)
	}
	if w := g.designTool("design_get", "design-cap-2", `{}`); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "artifact_run_limit") {
		t.Fatalf("second distinct standing read over the cap: %d %s", w.Code, w.Body)
	}
	got := map[string]string{}
	for _, r := range runAuditRows(t, g) {
		got[r.Page+"/"+r.Operation] = r.Reach
	}
	if got[consented+"/write"] != "consented" || got[first+"/read"] != "standing" || got[second+"/read"] != "" {
		t.Fatalf("audit %v", got)
	}
}

// Fail closed: without its audit row, neither a read nor a write happens.
func TestRunScopeDesign_AuditFailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER run_audit_ignore BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			g := newDesignRunRig(t)
			id := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-closed")
			g.start(t, id)
			mustExec(t, g.f.rig.db, trigger)
			before := designVersionCount(t, g.f, id)
			if w := g.designTool("design_get", "design-closed", `{}`); w.Code == 200 {
				t.Fatal("read succeeded without its audit")
			}
			if w := g.designTool("design_apply", "design-closed", designRunOp); w.Code == 200 {
				t.Fatal("write succeeded without its audit")
			}
			if after := designVersionCount(t, g.f, id); after != before {
				t.Fatalf("refused write left a version: %d -> %d", before, after)
			}
		})
	}
}

// Flag off: an attached Design is unreachable without the exact-run consent,
// as today, and nothing is audited.
func TestRunScopeDesign_FlagOffUnchanged(t *testing.T) {
	g := newDesignRunRig(t)
	id := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-flag-off")
	g.start(t, id)
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if w := g.designTool("design_get", "design-flag-off", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("flag off without consent: %d %s", w.Code, w.Body)
	}
	mustExec(t, g.f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at,? FROM artifact_run_principals WHERE run_id=?`, id, g.f.a.RunID)
	if w := g.designTool("design_get", "@"+id, `{}`); w.Code != 200 {
		t.Fatalf("flag off consent read: %d %s", w.Code, w.Body)
	}
	if rows := runAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// Amendment 7 with role-only edit: a workspace ADMIN who holds only a read
// grant reads as an ordinary member, so the run gets the shared version; the
// ADMIN role's manager read of every Design is never delegated.
func TestRunScopeDesign_AdminWithReadGrantReadsTheSharedVersion(t *testing.T) {
	g := newDesignRunRig(t)
	mustExec(t, g.f.rig.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, g.f.a.WorkspaceID, g.f.a.HumanUserID)
	g.f.receipt = artifactRunFreshReceipt(t, g.f)
	id := g.design(t, g.f.rig.adminID, "OWNER", "design-admin-read")
	if w := g.designHuman(t, g.router.pages.ApplyDesign, g.f.rig.adminID, "OWNER", "design-admin-read", designRunOp); w.Code != 200 {
		t.Fatalf("owner edit: %d %s", w.Code, w.Body)
	}
	g.grant(t, id, "user", g.f.a.HumanUserID, "read")
	mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=1 WHERE page_id=?`, id)
	g.start(t, id)
	w := g.designTool("design_get", "design-admin-read", `{}`)
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if got := designVersionOf(t, w); got != 1 {
		t.Fatalf("an ADMIN's run with a read grant read version %d, want the shared 1", got)
	}
	if w := g.designTool("design_get", "design-admin-read", `{"version":2}`); w.Code == 200 {
		t.Fatalf("an ADMIN's run read the unshared latest: %s", w.Body)
	}
}

// The project family gate applies to artifact projects only: with the
// artifact_projects family off and Design on, a Design run works and an
// artifact-project run is refused, both at the tool and at the run gate.
func TestRunScopeDesign_ProjectFamilyOffKeepsDesignAndRefusesProjects(t *testing.T) {
	g := newDesignRunRig(t)
	design := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-family")
	project := g.create(t, g.f.a.HumanUserID, "MEMBER", "project-family")
	g.start(t, design, project)
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
	mustExec(t, g.f.rig.db, `UPDATE feature_flags SET enabled=0 WHERE key='artifact_projects'`)
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE flag_id IN (SELECT id FROM feature_flags WHERE key='artifact_projects')`)
	if w := g.designTool("design_get", "design-family", `{}`); w.Code != 200 {
		t.Fatalf("Design run with the project family off: %d %s", w.Code, w.Body)
	}
	if w := g.tool("artifact_versions", "project-family", `{}`); w.Code == 200 {
		t.Fatalf("artifact-project run with the project family off: %s", w.Body)
	}
	run := g.f.a
	if err := g.router.pages.checkScopedArtifactRun(t.Context(), &run, "pages:read"); err == nil {
		t.Fatal("the run gate admitted an artifact-project run with the family off")
	}
	tx, err := g.f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rec := &pageRecord{ID: project, OwnerUserID: g.f.a.HumanUserID}
	if _, err := g.router.pages.authorizeScopedArtifactRun(t.Context(), tx, &run, g.f.a.WorkspaceID, rec, false, false); err == nil {
		t.Fatal("the artifact scope admitted a project read with the family off")
	}
}

// An agent design_apply always saves a version, even when it changes no node,
// so it is a write: recorded once with its reach, like any other write
// authorization (spec §7.1c item 11).
func TestRunScopeDesign_ApplyWithoutChangesIsAVersionAndAWrite(t *testing.T) {
	g := newDesignRunRig(t)
	id := g.design(t, g.f.a.HumanUserID, "MEMBER", "design-noop")
	if w := g.designHuman(t, g.router.pages.ApplyDesign, g.f.a.HumanUserID, "MEMBER", "design-noop", designRunOp); w.Code != 200 {
		t.Fatalf("human edit: %d %s", w.Code, w.Body)
	}
	g.start(t, id)
	before := designVersionCount(t, g.f, id)
	if w := g.designTool("design_apply", "design-noop", designRunOp); w.Code != 200 {
		t.Fatalf("unchanged apply: %d %s", w.Code, w.Body)
	}
	if after := designVersionCount(t, g.f, id); after != before+1 {
		t.Fatalf("an unchanged apply: versions %d -> %d, want one more", before, after)
	}
	rows := runAuditRows(t, g)
	if len(rows) != 1 {
		t.Fatalf("want one write record, got %+v", rows)
	}
	g.wantAudit(t, rows[0], id, "referenced", "write")
}
