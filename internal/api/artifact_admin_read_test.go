package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/slides"
)

// PRD §5 (owner decision 30. 10.), #4825: a workspace OWNER or ADMIN reads
// every artifact in the workspace, including another member's unshared
// private output, of every type. Every such read of an artifact the reader
// neither owns nor holds a grant for is audited. No per-type exceptions.
// Grantees keep the shared version; the administrator read is never write,
// share or custody power.

func adminReadAudits(t *testing.T, h *PageHandler, action, user, page string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND user_id=? AND entity_type='page' AND entity_id=?`, action, user, page).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every metadata/content read surface of a private Page, Word document and
// Page project, per workspace role. On main an ADMIN got 404 on all of them.
func TestWorkspaceAdminReadsPrivateArtifactOnEveryReadSurface(t *testing.T) {
	roles := []struct {
		role    string
		allowed bool
	}{
		{"OWNER", true},
		{"ADMIN", true},
		{"MANAGER", false},
		{"MEMBER", false},
		{"VIEWER", false},
	}
	for _, tc := range roles {
		t.Run(tc.role, func(t *testing.T) {
			h, ws, _, id := privateReadSurfaceFixture(t)
			reader := "reader-" + strings.ToLower(tc.role)
			pagesSeedUser(t, h, ws, reader, reader+"@example.test", tc.role)
			for _, surface := range append([]string{"page"}, privateReadSurfaces...) {
				before := adminReadAudits(t, h, auditActionPrivateArtifactRead, reader, id)
				var got *httptest.ResponseRecorder
				if surface == "page" {
					got = pagesGetStatus(t, h, ws, reader, tc.role, "private-read")
				} else {
					got = privateReadSurfaceCall(t, h, ws, reader, tc.role, surface)
				}
				after := adminReadAudits(t, h, auditActionPrivateArtifactRead, reader, id)
				if !tc.allowed {
					if got.Code != 404 || strings.Contains(got.Body.String(), "Private read output") {
						t.Errorf("%s: %s HTTP%d want404 without metadata", tc.role, surface, got.Code)
					}
					if after != before {
						t.Errorf("%s: %s refused read wrote an access audit", tc.role, surface)
					}
					continue
				}
				if got.Code != 200 {
					t.Errorf("%s: %s HTTP%d want200: %s", tc.role, surface, got.Code, got.Body)
				}
				if after != before+1 {
					t.Errorf("%s: %s released private output with %d audits, want exactly one", tc.role, surface, after-before)
				}
				if surface == "document" && strings.Contains(got.Body.String(), `"can_write":true`) && tc.role == "ADMIN" {
					t.Errorf("ADMIN read advertised document write authority")
				}
			}
			listed := pagesSlugListed(pagesListSlugs(t, h, ws, reader, tc.role), "private-read")
			if listed != tc.allowed {
				t.Errorf("%s: catalog visible=%v want %v", tc.role, listed, tc.allowed)
			}
		})
	}
}

// The read never becomes write power over another custodian's private output.
func TestWorkspaceAdminPrivateReadGrantsNoWrite(t *testing.T) {
	h, ws, _, id := privateReadSurfaceFixture(t)
	pagesSeedUser(t, h, ws, "reader-admin", "reader-admin@example.test", "ADMIN")
	for _, door := range []string{"document", "project", "page"} {
		r := pagesRequest(t, http.MethodPost, "/private-read/"+door, ws, "reader-admin", "ADMIN", `{}`)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindSession))
		r.SetPathValue("slug", "private-read")
		out := httptest.NewRecorder()
		switch door {
		case "document":
			h.MutateDocument(out, r)
		case "project":
			r.Method = http.MethodPut
			h.PutProject(out, r)
		case "page":
			r.Method = http.MethodDelete
			h.Delete(out, r)
		}
		if out.Code != 404 {
			t.Errorf("ADMIN %s mutation of private output HTTP%d want404: %s", door, out.Code, out.Body)
		}
	}
	rec := &pageRecord{ID: id, OwnerUserID: "someone-else", PrivateAudience: true}
	if h.mayEditSpec(t.Context(), ws, "reader-admin", "ADMIN", rec) || h.mayAdministerGrants(t.Context(), ws, "reader-admin", "ADMIN", rec) {
		t.Error("ADMIN read audience became spec or grant authority over private output")
	}
	if ok, _ := h.mayProduce(t.Context(), ws, "reader-admin", "ADMIN", rec, &panelRecord{PanelID: "p", ProducerKind: "script"}); ok {
		t.Error("ADMIN read audience became production authority over private output")
	}
	var exists bool
	if err := h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pages WHERE id=?)`, id).Scan(&exists); err != nil || !exists {
		t.Fatalf("private output removed by ADMIN: %v", err)
	}
}

// An ordinary (non-private) artifact reached only through the administrator
// role is audited; one reached through the owning crew is not. On main no
// read of an ordinary Page or Word document by OWNER/ADMIN was audited.
func TestWorkspaceAdminReadOfOrdinaryArtifactIsAudited(t *testing.T) {
	cases := []struct {
		name, role string
		crew       bool
		audited    bool
	}{
		{"admin-role-only", "ADMIN", false, true},
		{"owner-role-only", "OWNER", false, true},
		{"admin-in-owning-crew", "ADMIN", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, _, id := privateReadSurfaceFixture(t)
			execOrFatal(t, h.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
			reader := "reader-" + tc.name
			pagesSeedUser(t, h, ws, reader, reader+"@example.test", tc.role)
			if tc.crew {
				pagesJoinCrew(t, h, reader, "crew-lookout")
			}
			for _, surface := range append([]string{"page"}, privateReadSurfaces...) {
				before := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, reader, id)
				var got *httptest.ResponseRecorder
				if surface == "page" {
					got = pagesGetStatus(t, h, ws, reader, tc.role, "private-read")
				} else {
					got = privateReadSurfaceCall(t, h, ws, reader, tc.role, surface)
				}
				if got.Code != 200 {
					t.Errorf("%s HTTP%d want200: %s", surface, got.Code, got.Body)
				}
				want := before
				if tc.audited {
					want++
				}
				if after := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, reader, id); after != want {
					t.Errorf("%s audits=%d want %d", surface, after-before, want-before)
				}
			}
			before := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, reader, id)
			if !pagesSlugListed(pagesListSlugs(t, h, ws, reader, tc.role), "private-read") {
				t.Fatal("administrator catalog lost an ordinary artifact")
			}
			want := before
			if tc.audited {
				want++
			}
			if after := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, reader, id); after != want {
				t.Errorf("catalog audits=%d want %d", after-before, want-before)
			}
		})
	}
	// A member outside the owning crew without a grant still cannot read it,
	// and the owner's own reads are never audited.
	h, ws, owner, id := privateReadSurfaceFixture(t)
	execOrFatal(t, h.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
	pagesSeedUser(t, h, ws, "outsider", "outsider@example.test", "MEMBER")
	if got := pagesGetStatus(t, h, ws, "outsider", "MEMBER", "private-read"); got.Code != 404 {
		t.Errorf("outsider HTTP%d want404", got.Code)
	}
	if got := pagesGetStatus(t, h, ws, owner, "MEMBER", "private-read"); got.Code != 200 {
		t.Fatalf("owner HTTP%d", got.Code)
	}
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action IN (?,?)`, auditActionWorkspaceAdminArtifactRead, auditActionPrivateArtifactRead).Scan(&n); err != nil || n != 0 {
		t.Errorf("owner/outsider produced %d administrator audits (%v)", n, err)
	}
	// Audit failure refuses the read instead of releasing it unaudited.
	pagesSeedUser(t, h, ws, "audited-admin", "audited-admin@example.test", "ADMIN")
	execOrFatal(t, h.db, `CREATE TRIGGER deny_admin_read BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.workspace_admin_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if got := pagesGetStatus(t, h, ws, "audited-admin", "ADMIN", "private-read"); got.Code != 500 || strings.Contains(got.Body.String(), "Private read output") {
		t.Errorf("unaudited administrator read released: HTTP%d", got.Code)
	}
}

// Artifact Projects (the engine Slides and Dashboard data build on): on main
// a private Project was readable only by its owner, and after sharing an
// administrator saw only the shared pin with no audit.
func TestWorkspaceAdminReadsEveryArtifactProjectVersion(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	for _, u := range []struct{ id, role string }{{"project-admin", "ADMIN"}, {"project-wsowner", "OWNER"}, {"project-member", "MEMBER"}, {"project-reader", "MEMBER"}} {
		pagesSeedUser(t, h, ws, u.id, u.id+"@example.test", u.role)
	}
	read := func(user, query string) *httptest.ResponseRecorder {
		return call(user, "GET", "three-screens/artifact"+query, "")
	}
	// Unshared private Project.
	for _, admin := range []string{"project-admin", "project-wsowner"} {
		got := read(admin, "")
		if got.Code != 200 || !strings.Contains(got.Body.String(), "Version one") {
			t.Fatalf("%s private Project read %d %s", admin, got.Code, got.Body)
		}
		if n := adminReadAudits(t, h, auditActionPrivateArtifactRead, admin, page); n != 1 {
			t.Errorf("%s private Project audits=%d want 1", admin, n)
		}
	}
	if got := read("project-member", ""); got.Code != 404 {
		t.Errorf("member private Project read %d", got.Code)
	}
	// Owner shares v1, grants a reader, then publishes v2 (unshared).
	if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("share %d %s", got.Code, got.Body)
	}
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user','project-reader','read',?,datetime('now'))`, page, owner)
	if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); got.Code != 200 {
		t.Fatalf("v2 %d %s", got.Code, got.Body)
	}
	got := read("project-reader", "")
	if got.Code != 200 || strings.Contains(got.Body.String(), "Version two") || !strings.Contains(got.Body.String(), "Version one") {
		t.Fatalf("grantee must stay on the shared version: %d %s", got.Code, got.Body)
	}
	if got := read("project-reader", "?version=2"); got.Code == 200 {
		t.Fatalf("grantee read the unshared version: %s", got.Body)
	}
	if got := read("project-member", ""); got.Code != 404 {
		t.Errorf("ungranted member read %d", got.Code)
	}
	for _, admin := range []string{"project-admin", "project-wsowner"} {
		before := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, admin, page)
		latest := read(admin, "")
		var view struct {
			LatestVersion int64 `json:"latest_version"`
			CanWrite      bool  `json:"can_write"`
			CanShare      bool  `json:"can_share"`
			CanRestore    bool  `json:"can_restore"`
			Manifest      struct {
				Version int64 `json:"version"`
			} `json:"manifest"`
			Versions []json.RawMessage `json:"versions"`
		}
		if latest.Code != 200 || json.Unmarshal(latest.Body.Bytes(), &view) != nil || view.Manifest.Version != 2 || !strings.Contains(latest.Body.String(), "Version two") {
			t.Fatalf("%s latest read %d %s", admin, latest.Code, latest.Body)
		}
		if len(view.Versions) != 2 {
			t.Errorf("%s history has %d versions, want 2", admin, len(view.Versions))
		}
		if view.CanWrite || view.CanShare || view.CanRestore {
			t.Errorf("%s read advertised write/share/restore: %+v", admin, view)
		}
		if old := read(admin, "?version=1"); old.Code != 200 || !strings.Contains(old.Body.String(), "Version one") {
			t.Errorf("%s exact version read %d", admin, old.Code)
		}
		if after := adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, admin, page); after < before+2 {
			t.Errorf("%s shared Project reads audited %d times, want one per read", admin, after-before)
		}
		for _, suffix := range []string{"versions", "restore", "share"} {
			out := call(admin, "POST", "three-screens/artifact/"+suffix, artifactProjectBody(2, "admin write"))
			if out.Code != 403 && out.Code != 404 {
				t.Errorf("%s %s HTTP%d", admin, suffix, out.Code)
			}
		}
	}
	var latest, shared int64
	if err := h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest, &shared); err != nil || latest != 2 || shared != 1 {
		t.Fatalf("administrator read changed the head: %d %d %v", latest, shared, err)
	}
	var grantee int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE user_id IN (?, 'project-reader') AND action IN (?,?)`, owner, auditActionPrivateArtifactRead, auditActionWorkspaceAdminArtifactRead).Scan(&grantee); err != nil || grantee != 0 {
		t.Errorf("owner/grantee reads were audited as administrator access: %d %v", grantee, err)
	}
}

// Sheet (workbook) and Slides through the real router, and the audit listing
// the CLI (`crewship audit --action <name>`) drives.
func TestWorkspaceAdminReadsPrivateSheetAndSlidesAndAuditLists(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "mia", "mia@ex.com", "ws-old", "MEMBER")
	mia := mintTokenFor(t, f.db, "mia", "instfixmia000000000000000000")
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	f.r.RegisterSlidesHuman(true)

	wmSeedPage(t, f.db, "ws-old", "boss-workbook", "boss-workbook", "Boss workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='boss-workbook'`)
	body := `{"mutation_id":"private-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	book := "/api/v1/pages/boss-workbook/workbook"
	wantCode(t, f.do(f.boss, "POST", book+"/operations?workspace_id=ws-old", body), 200, "custodian import")
	wantCode(t, f.do(f.wsAdmin, "GET", book+"?workspace_id=ws-old", ""), 200, "ADMIN private workbook read")
	wantCode(t, f.do(mia, "GET", book+"?workspace_id=ws-old", ""), 404, "member private workbook read")
	if got := f.do(f.wsAdmin, "POST", book+"/operations?workspace_id=ws-old", strings.Replace(body, "private-import", "admin-import", 1)); got.Code == 200 {
		t.Fatalf("ADMIN wrote a private workbook: %s", got.Body)
	}

	wmSeedPage(t, f.db, "ws-old", "boss-slides", "boss-slides", "Boss slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='boss-slides'`)
	deck := humanSlidesDeck(t, "boss-slides")
	init := humanSlidesJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})
	deckPath := "/api/v1/pages/boss-slides/slides"
	wantCode(t, f.do(f.boss, "POST", deckPath+"/initialize?workspace_id=ws-old", init), 200, "custodian initialize")
	wantCode(t, f.do(f.wsAdmin, "GET", deckPath+"?workspace_id=ws-old", ""), 200, "ADMIN private Slides read")
	wantCode(t, f.do(f.wsAdmin, "GET", deckPath+"/revisions/1?workspace_id=ws-old", ""), 200, "ADMIN private Slides revision")
	if got := f.do(mia, "GET", deckPath+"?workspace_id=ws-old", ""); got.Code == 200 {
		t.Fatalf("member read private Slides: %s", got.Body)
	}

	for _, page := range []string{"boss-workbook", "boss-slides"} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND user_id='wsadmin' AND entity_id=?`, auditActionPrivateArtifactRead, page).Scan(&n); err != nil || n == 0 {
			t.Errorf("ADMIN read of %s not audited: %d %v", page, n, err)
		}
		var foreign int
		if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND user_id='mia' AND entity_id=?`, auditActionPrivateArtifactRead, page).Scan(&foreign); err != nil || foreign != 0 {
			t.Errorf("refused member read was audited: %d %v", foreign, err)
		}
	}
	listing := f.do(f.boss, "GET", "/api/v1/audit?workspace_id=ws-old&action="+auditActionPrivateArtifactRead, "")
	wantCode(t, listing, 200, "audit listing by action")
	for _, page := range []string{"boss-workbook", "boss-slides"} {
		if !strings.Contains(listing.Body.String(), page) {
			t.Errorf("audit listing misses the ADMIN read of %s: %s", page, listing.Body)
		}
	}
}
