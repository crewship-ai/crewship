package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// The Edit predicate for Dashboard data (#4732) is exactly the source-save
// predicate: an explicit Page write grant or an inherited folder write. These
// boundaries complete the matrix in pages_artifact_data_edit_test.go: folder
// inheritance, plain workspace roles without a grant, the private-audience
// seal, unchanged reader reads, the audited administrator read, and storage
// admission for editor writes. Every refusal leaves stored data unchanged.
const dashboardEditWrite = `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`

func dashboardEditMember(t *testing.T, h *PageHandler, ws, user, role string) {
	t.Helper()
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, generateCUID(), ws, user, role)
}

func dashboardEditFolder(t *testing.T, h *PageHandler, ws, page, user string, write bool) {
	t.Helper()
	var crew string
	if err := h.db.QueryRow(`SELECT id FROM crews WHERE workspace_id=? LIMIT 1`, ws).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('data-folder',?,'data-folder','Folder',?,datetime('now'),datetime('now'))`, ws, crew)
	mustExec(t, h.db, `UPDATE pages SET folder_id='data-folder' WHERE id=?`, page)
	mustExec(t, h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('data-folder','user',?,1,?,datetime('now'))`, user, write)
}

func TestDashboardDataEditPredicateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    string
		setup   func(t *testing.T, h *PageHandler, ws, page string)
		code    int
		written bool
	}{
		{name: "folder write editor writes latest", user: "data-folder-editor", code: 200, written: true, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			dashboardEditMember(t, h, ws, "data-folder-editor", "MEMBER")
			dashboardEditFolder(t, h, ws, page, "data-folder-editor", true)
		}},
		{name: "folder read-only is refused", user: "data-folder-reader", code: 403, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			dashboardEditMember(t, h, ws, "data-folder-reader", "MEMBER")
			dashboardEditFolder(t, h, ws, page, "data-folder-reader", false)
		}},
		{name: "member without grant sees absent", user: "data-member", code: 404, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			dashboardEditMember(t, h, ws, "data-member", "MEMBER")
		}},
		{name: "admin without grant is refused", user: "data-admin", code: 403, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			dashboardEditMember(t, h, ws, "data-admin", "ADMIN")
		}},
		{name: "edit grant behind private audience sees absent", user: "data-editor", code: 404, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		}},
		{name: "folder editor behind private audience sees absent", user: "data-folder-editor", code: 404, setup: func(t *testing.T, h *PageHandler, ws, page string) {
			dashboardEditMember(t, h, ws, "data-folder-editor", "MEMBER")
			dashboardEditFolder(t, h, ws, page, "data-folder-editor", true)
			mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, _, page := dashboardEditFixture(t)
			tc.setup(t, h, ws, page)
			before := dashboardDataSnapshot(t, h, page)
			w := dashboardRequest(t, h, ws, tc.user, "POST", "sales", "", dashboardEditWrite)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body)
			}
			after := dashboardDataSnapshot(t, h, page)
			if !tc.written {
				if after != before {
					t.Fatalf("refused write changed data:\n%s\n->\n%s", before, after)
				}
				if strings.Contains(w.Body.String(), "seed") || strings.Contains(w.Body.String(), page) {
					t.Fatalf("refusal leaked data: %s", w.Body)
				}
				return
			}
			var actor string
			if err := h.db.QueryRow(`SELECT updated_by FROM artifact_data_documents WHERE page_id=? AND id='row'`, page).Scan(&actor); err != nil || actor != tc.user {
				t.Fatalf("actor %q want %q: %v", actor, tc.user, err)
			}
		})
	}
}

// Granting writes did not widen reads: Read/Comment holders still see only
// the shared pin, an editor only the latest head.
func TestDashboardDataEditReadersUnchanged(t *testing.T) {
	h, ws, _, _ := dashboardEditFixture(t)
	for _, tc := range []struct {
		user, query string
		code        int
	}{
		{"data-reader", "?version=1", 200},
		{"data-reader", "?version=2", 409},
		{"data-commenter", "?version=1", 200},
		{"data-commenter", "?version=2", 409},
		{"data-editor", "?version=2", 200},
		{"data-editor", "?version=1", 409},
		{"data-revoked", "?version=1", 404},
		{"data-stranger", "?version=1", 404},
	} {
		if w := dashboardRequest(t, h, ws, tc.user, "GET", "sales", tc.query, ""); w.Code != tc.code {
			t.Errorf("%s GET %s = %d, want %d: %s", tc.user, tc.query, w.Code, tc.code, w.Body)
		}
	}
}

// PRD §5: a workspace ADMIN without a grant reads the private latest head,
// audited, but an administrator read never becomes write authority.
func TestDashboardDataAdminReadAuditedButNotWrite(t *testing.T) {
	h, ws, _, page := dashboardEditFixture(t)
	dashboardEditMember(t, h, ws, "data-admin", "ADMIN")
	audits := func() int {
		var n int
		if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND user_id='data-admin'`, auditActionWorkspaceAdminArtifactRead).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := audits()
	if w := dashboardRequest(t, h, ws, "data-admin", "GET", "sales", "?version=2", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "seed") {
		t.Fatalf("admin read latest: %d %s", w.Code, w.Body)
	}
	if got := audits(); got != start+1 {
		t.Fatalf("admin read audit rows %d, want %d", got, start+1)
	}
	before := dashboardDataSnapshot(t, h, page)
	if w := dashboardRequest(t, h, ws, "data-admin", "POST", "sales", "", dashboardEditWrite); w.Code != 403 {
		t.Fatalf("admin write: %d %s", w.Code, w.Body)
	}
	if after := dashboardDataSnapshot(t, h, page); after != before {
		t.Fatal("admin write changed data")
	}
}

// Editor writes take the same storage admission as owner writes: the free-disk
// reserve and the workspace artifact budget refuse with the shared storage-full
// response, write nothing, and the same request succeeds once space returns.
func TestDashboardDataEditorWriteHonoursStorageAdmission(t *testing.T) {
	for _, limit := range []string{"disk reserve", "workspace budget"} {
		t.Run(limit, func(t *testing.T) {
			h, ws, _, page := dashboardEditFixture(t)
			var restore func()
			if limit == "disk reserve" {
				mustExec(t, h.db, `UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, int64(1)<<62)
				restore = func() { mustExec(t, h.db, `UPDATE artifact_storage_global SET min_free_bytes=0 WHERE id=1`) }
			} else {
				var max int64
				if err := h.db.QueryRow(`SELECT max_bytes FROM artifact_storage_workspaces WHERE workspace_id=?`, ws).Scan(&max); err != nil {
					t.Fatal(err)
				}
				mustExec(t, h.db, `UPDATE artifact_storage_workspaces SET max_bytes=used_bytes+1 WHERE workspace_id=?`, ws)
				restore = func() {
					mustExec(t, h.db, `UPDATE artifact_storage_workspaces SET max_bytes=? WHERE workspace_id=?`, max, ws)
				}
			}
			before := dashboardDataSnapshot(t, h, page)
			got := dashboardRequest(t, h, ws, "data-editor", "POST", "sales", "", dashboardEditWrite)
			want := httptest.NewRecorder()
			artifactStorageRefusal(want, artifacts.ErrStorageQuota)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Fatalf("editor write past %s = %d %s, want %d %s", limit, got.Code, got.Body, want.Code, want.Body)
			}
			if after := dashboardDataSnapshot(t, h, page); after != before {
				t.Fatalf("refused editor write changed data past %s", limit)
			}
			restore()
			if w := dashboardRequest(t, h, ws, "data-editor", "POST", "sales", "", dashboardEditWrite); w.Code != 200 {
				t.Fatalf("same editor write after space returned: %d %s", w.Code, w.Body)
			}
		})
	}
}

// An agent run acts only for the Dashboard owner. A run whose human holds an
// Edit grant on someone else's Dashboard is refused, although that human may
// write directly; the refusal writes nothing.
func TestDashboardDataRunOfNonOwnerEditorRefused(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	f.pages.artifactMasterToken = "synthetic-dashboard-editor-run-master"
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "dashboard-editor-run")
	human := func(path, slug, collection, body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", path, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
		r.Header.Set("Authorization", "Bearer "+humanToken)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.SetPathValue("slug", slug)
		r.SetPathValue("collection", collection)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := human("/api/v1/pages/artifacts/consent", "", "", `{"run_id":"principal-run"}`, f.pages.GrantArtifactProjectRunConsent); w.Code != 200 {
		t.Fatalf("human consent: %d %s", w.Code, w.Body)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	tool := func(operation, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":"editor-dashboard","payload":%s}`, operation, payload)))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		w := httptest.NewRecorder()
		f.pages.ArtifactProjectTool(w, r.WithContext(ctx))
		return w
	}
	if w := tool("artifact_create", `{"name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatalf("run create: %d %s", w.Code, w.Body)
	}
	var page string
	if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='editor-dashboard'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	batch := func(id string) string {
		return `{"collection":"sales","base_version":1,"documents":[{"id":"` + id + `","ifVersion":0,"value":{"amount":1}}]}`
	}
	// Calibration: while the run's human owns the Dashboard, the run writes.
	if w := tool("artifact_data_batch", batch("owner-run")); w.Code != 200 {
		t.Fatalf("owner run write: %d %s", w.Code, w.Body)
	}
	// The Dashboard now belongs to someone else; the run's human keeps an Edit grant.
	mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES('data-new-owner','data-new-owner@example.test')`)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'data-new-owner','MEMBER')`, generateCUID(), f.a.WorkspaceID)
	// A run created from a private chat seals its output; lift the seal so the
	// Edit grant is the only thing that decides.
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id='data-new-owner',private_audience=0 WHERE id=?`, page)
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write','data-new-owner',datetime('now'))`, page, f.a.HumanUserID)
	var before int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_data_documents WHERE page_id=?`, page).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Same refusal as the source-save path for this run: data writes use exactly
	// its predicate, which admits a run only for the Page owner.
	publish := tool("artifact_publish", dashboardBody(1, "sales"))
	if publish.Code != 403 {
		t.Fatalf("source save by run of non-owner editor: %d %s", publish.Code, publish.Body)
	}
	if w := tool("artifact_data_batch", batch("editor-run")); w.Code != publish.Code {
		t.Fatalf("data write by run of non-owner editor: %d %s, source save %d", w.Code, w.Body, publish.Code)
	}
	var latest int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("refused run publish changed source: %d %v", latest, err)
	}
	var after int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_data_documents WHERE page_id=?`, page).Scan(&after); err != nil || after != before {
		t.Fatalf("refused run write changed data: %d -> %d %v", before, after, err)
	}
	// The same human, acting directly with the Edit grant, may write.
	if w := human("/api/v1/pages/editor-dashboard/artifact/data/sales", "editor-dashboard", "sales", `{"base_version":1,"documents":[{"id":"editor-human","ifVersion":0,"value":{"amount":2}}]}`, f.pages.BatchArtifactData); w.Code != 200 {
		t.Fatalf("direct editor write: %d %s", w.Code, w.Body)
	}
}
