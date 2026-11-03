package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
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

// Review round 2 (#4825): the administrator read is independent of every
// other grant. An Edit or Read grant only adds rights; it never narrows a
// workspace OWNER/ADMIN back to the latest or shared version. A read the
// reader's own grant already serves (an editor on the latest, a grantee on the
// shared pin) writes no administrator audit; a read reachable only through
// the role (any other version, the full history) writes exactly one.
// On 73fcdcf4 an ADMIN holding an Edit grant got 404 for v1 on Slides and
// dashboard data and a latest-only Project; a Read grant hid nothing there
// but the reads it could not have made without the role went unaudited.
func TestWorkspaceAdminReadIsIndependentOfGrants(t *testing.T) {
	type surface struct {
		name  string
		setup func(t *testing.T, h *PageHandler, owner string, call func(string, string, string, string) *httptest.ResponseRecorder)
		read  func(t *testing.T, h *PageHandler, ws, user, page string, version int) (int, string)
		v1    string
		v2    string
	}
	surfaces := []surface{{
		name: "project",
		setup: func(t *testing.T, h *PageHandler, owner string, call func(string, string, string, string) *httptest.ResponseRecorder) {
			if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
				t.Fatalf("share %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); w.Code != 200 {
				t.Fatalf("v2 %d %s", w.Code, w.Body)
			}
		},
		v1: "Version one", v2: "Version two",
	}, {
		name: "slides",
		setup: func(t *testing.T, h *PageHandler, owner string, call func(string, string, string, string) *httptest.ResponseRecorder) {
			t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
			if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Slides",`+strings.TrimPrefix(slidesArtifactBody(0, "Deck one"), "{")); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
				t.Fatalf("share %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/versions", slidesArtifactBody(1, "Deck two")); w.Code != 200 {
				t.Fatalf("v2 %d %s", w.Code, w.Body)
			}
		},
		read: func(t *testing.T, h *PageHandler, ws, user, page string, version int) (int, string) {
			w := httptest.NewRecorder()
			h.GetArtifactSlides(w, slidesAPIRequest(t, h, ws, user, page, fmt.Sprintf("slides?version=%d", version)))
			return w.Code, w.Body.String()
		},
		v1: "Deck one", v2: "Deck two",
	}, {
		name: "dashboard data",
		setup: func(t *testing.T, h *PageHandler, owner string, call func(string, string, string, string) *httptest.ResponseRecorder) {
			t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
			if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
				t.Fatalf("share %d %s", w.Code, w.Body)
			}
			if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(1, "sales")); w.Code != 200 {
				t.Fatalf("v2 %d %s", w.Code, w.Body)
			}
			var ws string
			if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE slug='three-screens'`).Scan(&ws); err != nil {
				t.Fatal(err)
			}
			if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`); w.Code != 200 {
				t.Fatalf("data %d %s", w.Code, w.Body)
			}
		},
		read: func(t *testing.T, h *PageHandler, ws, user, page string, version int) (int, string) {
			w := dashboardRequest(t, h, ws, user, "GET", "", fmt.Sprintf("?version=%d", version), "")
			return w.Code, w.Body.String()
		},
		v1: `"code_version":1,"collections":[{"name":"sales"`, v2: `"code_version":2,"collections":[{"name":"sales"`,
	}}
	for _, s := range surfaces {
		for _, grant := range []string{"write", "read"} {
			for _, role := range []string{"ADMIN", "OWNER"} {
				t.Run(s.name+"/"+role+"+"+grant+" grant", func(t *testing.T) {
					h, ws, owner, call := artifactProjectFixture(t)
					s.setup(t, h, owner, call)
					var page string
					if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
						t.Fatal(err)
					}
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
					admin := "granted-admin"
					pagesSeedUser(t, h, ws, admin, admin+"@example.test", role)
					execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,?,?,datetime('now'))`, page, admin, grant, owner)
					audits := func() int {
						return adminReadAudits(t, h, auditActionWorkspaceAdminArtifactRead, admin, page)
					}
					// The version the grant itself serves, and the one only the role reaches.
					covered, beyond := 2, 1
					if grant == "read" {
						covered, beyond = 1, 2
					}
					if s.name == "project" {
						var view struct {
							Manifest struct {
								Version int64 `json:"version"`
							} `json:"manifest"`
							Versions []json.RawMessage `json:"versions"`
							CanShare bool              `json:"can_share"`
							Quota    struct {
								Retention string `json:"retention"`
							} `json:"quota"`
						}
						before := audits()
						for _, v := range []int{1, 2} {
							got := call(admin, "GET", fmt.Sprintf("three-screens/artifact?version=%d", v), "")
							want := s.v1
							if v == 2 {
								want = s.v2
							}
							if got.Code != 200 || !strings.Contains(got.Body.String(), want) {
								t.Fatalf("%s+%s grant read v%d: %d %s", role, grant, v, got.Code, got.Body)
							}
							if json.Unmarshal(got.Body.Bytes(), &view) != nil || len(view.Versions) != 2 || view.CanShare || view.Quota.Retention == "latest-version-only" {
								t.Errorf("%s+%s grant v%d narrowed the administrator read: %s", role, grant, v, got.Body)
							}
						}
						// Each response carries the whole history no grant reaches:
						// one administrator audit per request.
						if n := audits() - before; n != 2 {
							t.Errorf("%s+%s grant Project reads audited %d times, want 2", role, grant, n)
						}
						return
					}
					before := audits()
					code, body := s.read(t, h, ws, admin, page, covered)
					want := map[int]string{1: s.v1, 2: s.v2}
					if code != 200 || !strings.Contains(body, want[covered]) {
						t.Fatalf("%s+%s grant read of its own v%d: %d %s", role, grant, covered, code, body)
					}
					if n := audits() - before; n != 0 {
						t.Errorf("%s+%s grant read its own version and was audited %d times", role, grant, n)
					}
					code, body = s.read(t, h, ws, admin, page, beyond)
					if code != 200 || !strings.Contains(body, want[beyond]) {
						t.Fatalf("%s+%s grant narrowed the administrator read of v%d: %d %s", role, grant, beyond, code, body)
					}
					if n := audits() - before; n != 1 {
						t.Errorf("%s+%s grant read v%d only through the role, audited %d times, want 1", role, grant, beyond, n)
					}
					var latest, shared int64
					if err := h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest, &shared); err != nil || latest != 2 || shared != 1 {
						t.Fatalf("administrator read changed the head: %d %d %v", latest, shared, err)
					}
				})
			}
		}
	}
}

// Review round 3 (#4825): the administrator read of a private artifact is
// visible only where the audit listing allows it (workspace OWNER and the
// artifact's owner). It must not reach the workspace journal, which every
// member reads through GET /api/v1/journal (`crewship journal`). On 52cd78cd
// the private Project read was chained into journal_entries with the page,
// its owner and the reader.
func TestWorkspaceAdminPrivateReadStaysOutOfMemberJournal(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+strings.TrimPrefix(artifactProjectBody(0, "Secret plan"), "{")); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	pagesSeedUser(t, h, ws, "journal-admin", "journal-admin@example.test", "ADMIN")
	pagesSeedUser(t, h, ws, "journal-member", "journal-member@example.test", "MEMBER")
	pagesSeedUser(t, h, ws, "journal-wsowner", "journal-wsowner@example.test", "OWNER")
	for _, query := range []string{"", "?version=1"} {
		if got := call("journal-admin", "GET", "three-screens/artifact"+query, ""); got.Code != 200 || !strings.Contains(got.Body.String(), "Secret plan") {
			t.Fatalf("ADMIN private Project read %d %s", got.Code, got.Body)
		}
	}
	// The compliance row is kept.
	if n := adminReadAudits(t, h, auditActionPrivateArtifactRead, "journal-admin", page); n != 2 {
		t.Fatalf("private administrator reads audited %d times, want 2", n)
	}
	journalHandler := NewJournalHandler(h.db, newTestLogger(), nil)
	auditHandler := NewAuditHandler(h.db, newTestLogger())
	for _, viewer := range []struct{ id, role string }{{"journal-member", "MEMBER"}, {"journal-admin", "ADMIN"}} {
		rr := httptest.NewRecorder()
		journalHandler.List(rr, withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal?workspace_id="+ws+"&limit=500", nil), viewer.id, ws, viewer.role))
		if rr.Code != 200 {
			t.Fatalf("%s journal list %d %s", viewer.id, rr.Code, rr.Body)
		}
		for _, secret := range []string{page, auditActionPrivateArtifactRead, "Workspace administrator read an artifact"} {
			if strings.Contains(rr.Body.String(), secret) {
				t.Errorf("%s journal reveals the private administrator read (%q): %s", viewer.id, secret, rr.Body)
			}
		}
		listing := httptest.NewRecorder()
		auditHandler.List(listing, withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/audit?workspace_id="+ws+"&action="+auditActionPrivateArtifactRead, nil), viewer.id, ws, viewer.role))
		if strings.Contains(listing.Body.String(), page) {
			t.Errorf("%s audit listing reveals the private administrator read: %s", viewer.id, listing.Body)
		}
	}
	var journaled int
	if err := h.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE workspace_id=? AND (json_extract(payload,'$.entity_id')=? OR json_extract(refs,'$.entity_id')=?) AND json_extract(payload,'$.action')=?`, ws, page, page, auditActionPrivateArtifactRead).Scan(&journaled); err != nil || journaled != 0 {
		t.Errorf("private administrator read published to the journal: %d %v", journaled, err)
	}
	// The workspace OWNER still lists it.
	listing := httptest.NewRecorder()
	auditHandler.List(listing, withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/audit?workspace_id="+ws+"&action="+auditActionPrivateArtifactRead, nil), "journal-wsowner", ws, "OWNER"))
	if listing.Code != 200 || !strings.Contains(listing.Body.String(), page) {
		t.Errorf("workspace OWNER cannot list the private administrator read: %d %s", listing.Code, listing.Body)
	}
}

// installPrivateReadAuditHook runs hook once, for pageID, at the seam between
// a content read that released its transaction and that read's audit decision.
func installPrivateReadAuditHook(t *testing.T, pageID string, hook func()) *int {
	t.Helper()
	fired := 0
	fn := func(id string) {
		if id == pageID && fired == 0 {
			fired++
			hook()
		}
	}
	privateReadAuditHook.Store(&fn)
	t.Cleanup(func() { privateReadAuditHook.Store(nil) })
	return &fired
}

// Review round 3 (#4825), blocker 1: the audit of an administrator's read of
// private Mooring content must be decided on the custodian the content was
// read under. On f7ccbad7 the read reloaded the Page after its transaction;
// a custody transfer to the reading ADMIN in that window made the reload name
// the ADMIN as owner, so the response released the previous custodian's
// content with no access record.
func TestWorkspaceAdminMooringReadAuditSurvivesConcurrentCustodyTransfer(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	created := f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", `{"app_id":"hello","slug":"custody-race","name":"Custody race"}`)
	wantCode(t, created, 201, "create")
	var page struct {
		ID string `json:"page_id"`
	}
	if json.Unmarshal(created.Body.Bytes(), &page) != nil || page.ID == "" {
		t.Fatal(created.Body.String())
	}
	fired := installPrivateReadAuditHook(t, page.ID, func() {
		// The workspace OWNER hands custody to the reading ADMIN after the
		// content snapshot of boss's document was taken.
		mustExec(t, f.db, `UPDATE pages SET owner_user_id='wsadmin' WHERE id=?`, page.ID)
	})
	read := f.do(f.wsAdmin, "GET", "/api/v1/pages/custody-race/mooring?workspace_id=ws-old", "")
	if *fired != 1 {
		t.Fatalf("custody transfer seam fired %d times", *fired)
	}
	audits := adminReadAudits(t, f.r.pages, auditActionPrivateArtifactRead, "wsadmin", page.ID)
	if read.Code == 200 && audits != 1 {
		t.Fatalf("ADMIN received boss's private Mooring content with %d access records", audits)
	}
	if read.Code != 200 && audits != 0 {
		t.Fatalf("refused read still audited: %d %d", read.Code, audits)
	}
	if read.Code == 200 {
		var meta string
		if err := f.db.QueryRow(`SELECT metadata FROM audit_logs WHERE action=? AND user_id='wsadmin' AND entity_id=?`, auditActionPrivateArtifactRead, page.ID).Scan(&meta); err != nil || !strings.Contains(meta, `"owner_user_id":"boss"`) {
			t.Fatalf("audit does not name the custodian the content was read under: %q %v", meta, err)
		}
	}
}

// The same seam for a native Word (Craft) document read: the record audited
// is the one authorized in the engine transaction that read the content.
func TestWorkspaceAdminNativeWordReadAuditSurvivesConcurrentCustodyTransfer(t *testing.T) {
	f, h, _ := craftNativeHTTPFixture(t)
	// ptdeparting reads as a workspace ADMIN another member's private document,
	// with a credential captured after the role change.
	mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
	mustExec(t, f.db, `UPDATE pages SET owner_user_id=?, private_audience=1 WHERE id='native-page'`, f.adminID)
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-admin-source',?,?)`, f.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	u := &AuthUser{ID: f.userID, SessionID: "native-admin-source", CredentialExpiresAt: time.Now().Add(time.Hour)}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
	cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"wordcraft": {Store: cfg, EditorURL: "/workbook-editor/b/" + cfg.EditorBundle + "/index.html"}})
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"secret":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.adminID, nil }); err != nil {
		t.Fatal(err)
	}
	token, _ := craftNativeIssued(t, h, ctx)
	baseline := craftNativeDocumentCall(t, d, ctx, token, "read", nil)
	if baseline.Code != 200 || adminReadAudits(t, h.pages, auditActionPrivateArtifactRead, f.userID, "native-page") != 1 {
		t.Fatalf("baseline audited ADMIN native read: %d %s", baseline.Code, baseline.Body)
	}
	fired := installPrivateReadAuditHook(t, "native-page", func() {
		mustExec(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id='native-page'`, f.userID)
	})
	read := craftNativeDocumentCall(t, d, ctx, token, "read", nil)
	if *fired != 1 {
		t.Fatalf("custody transfer seam fired %d times (read %d %s)", *fired, read.Code, read.Body)
	}
	audits := adminReadAudits(t, h.pages, auditActionPrivateArtifactRead, f.userID, "native-page")
	if read.Code == 200 && audits != 2 {
		t.Fatalf("ADMIN received another custodian's native document with %d access records (want 2)", audits)
	}
	if read.Code != 200 && audits != 1 {
		t.Fatalf("refused read changed audits: %d %d", read.Code, audits)
	}
}

// Review round 3 (#4825), blocker 2: an owner-issued Edit grant never narrows
// an administrator's Sheet read. On f7ccbad7 an ADMIN holding that grant was
// served as a named editor, so /commits below the publication floor returned
// 410 while the same ADMIN without the grant read the retained history.
func TestWorkspaceAdminSheetHistoryIsIndependentOfEditGrant(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	q := "?workspace_id=ws-old"
	base := liveWorkbook(t, f, ann, "ann", "admin-history")
	owner := openEditorSession(t, f, ann, base)
	wantCode(t, f.do(ann, "POST", base+"/edits"+q, cellEdit(owner.SessionID, 1, 1, "B2", "private-draft")), 200, "owner private edit")
	wantCode(t, f.do(ann, "POST", base+"/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish with bob")

	history := func(what string) string {
		t.Helper()
		feed := f.do(f.wsAdmin, "GET", base+"/commits"+q+"&since=0", "")
		wantCode(t, feed, 200, what)
		if !strings.Contains(feed.Body.String(), "private-draft") {
			t.Fatalf("%s misses retained history: %s", what, feed.Body.String())
		}
		return feed.Body.String()
	}
	audits := func() int {
		return adminReadAudits(t, f.r.pages, auditActionPrivateArtifactRead, "wsadmin", "admin-history")
	}
	without := history("ADMIN history without a grant")
	afterWithout := audits()
	if afterWithout < 1 {
		t.Fatalf("ADMIN history read without a grant not audited: %d", afterWithout)
	}

	wantCode(t, f.do(ann, "POST", base+"/editors"+q, `{"editor":"wsadmin@ex.com"}`), 200, "owner names the ADMIN editor")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM page_grants WHERE page_id='admin-history' AND subject_type='user' AND subject_id='wsadmin' AND level='write' AND granted_by_user_id='ann'`); n != 1 {
		t.Fatalf("owner-issued Edit grant rows = %d", n)
	}
	with := history("ADMIN history with an owner-issued Edit grant")
	if with != without {
		t.Fatalf("grant changed the administrator's history:\nwithout %s\nwith    %s", without, with)
	}
	if got := audits(); got != afterWithout+1 {
		t.Fatalf("ADMIN history read with a grant audits=%d, want %d", got, afterWithout+1)
	}
	// The grant still adds what it grants: a live editor session and writes.
	admin := openEditorSession(t, f, f.wsAdmin, base)
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/edits"+q, cellEdit(admin.SessionID, 1, int(admin.Rev), "C3", "admin-edit")), 200, "granted ADMIN edits")
}

// Coordinator follow-up (#4825): the Page spec history of a Page holding a
// Sheet was readable only by its owner (workbookPageOwnerOnly), a per-type
// exception to PRD §5. A workspace OWNER/ADMIN who does not own it now reads
// it through the audited administrator path; every other non-owner, including
// the Sheet's named editor, is still refused. A failed audit releases nothing
// and the private read never reaches the member-visible journal.
func TestWorkspaceAdminReadsSheetPageSpecHistory(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	q := "?workspace_id=ws-old"
	liveWorkbook(t, f, ann, "ann", "private-spec-book")
	liveWorkbook(t, f, ann, "ann", "shared-spec-book")
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/shared-spec-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish with bob")
	for _, id := range []string{"private-spec-book", "shared-spec-book"} {
		mustExec(t, f.db, `INSERT INTO page_versions(page_id,seq,spec_json,author_user_id,created_at) VALUES(?,1,'{"metadata":{"name":"secret-spec-name"},"spec":{"panels":[]}}','ann','2026-11-01T00:00:00Z')`, id)
	}
	versions := func(token, id string) *httptest.ResponseRecorder {
		return f.do(token, "GET", "/api/v1/pages/"+id+"/versions"+q, "")
	}
	wantCode(t, versions(ann, "private-spec-book"), 200, "owner history")
	wantCode(t, versions(ann, "shared-spec-book"), 200, "owner history (published)")
	for _, reader := range []struct{ token, user string }{{f.boss, "boss"}, {f.wsAdmin, "wsadmin"}} {
		for _, c := range []struct{ id, action string }{
			{"private-spec-book", auditActionPrivateArtifactRead},
			{"shared-spec-book", auditActionPrivateArtifactRead},
		} {
			before := adminReadAudits(t, f.r.pages, c.action, reader.user, c.id) + adminReadAudits(t, f.r.pages, auditActionWorkspaceAdminArtifactRead, reader.user, c.id)
			got := versions(reader.token, c.id)
			wantCode(t, got, 200, reader.user+" spec history of "+c.id)
			if !strings.Contains(got.Body.String(), "secret-spec-name") {
				t.Fatalf("%s history of %s misses the retained version: %s", reader.user, c.id, got.Body.String())
			}
			after := adminReadAudits(t, f.r.pages, c.action, reader.user, c.id) + adminReadAudits(t, f.r.pages, auditActionWorkspaceAdminArtifactRead, reader.user, c.id)
			if after != before+1 {
				t.Fatalf("%s spec history of %s audits %d -> %d, want one more", reader.user, c.id, before, after)
			}
		}
	}
	// The named editor and an unrelated member stay refused.
	if w := versions(bob, "shared-spec-book"); w.Code == 200 {
		t.Fatalf("Sheet editor read the Page spec history: %s", w.Body.String())
	}
	seedInstanceUser(t, f.db, "dave", "dave@ex.com", "ws-old", "MEMBER")
	dave := mintTokenFor(t, f.db, "dave", "workbookdave00000000000000")
	if w := versions(dave, "private-spec-book"); w.Code == 200 || strings.Contains(w.Body.String(), "secret-spec-name") {
		t.Fatalf("member read the private Sheet Page spec history: %s", w.Body.String())
	}
	// The private administrator read stays out of the member journal.
	if n := gridcraftCount(t, f, `SELECT count(*) FROM journal_entries WHERE payload LIKE '%private-spec-book%' AND entry_type LIKE '%accessed%'`); n != 0 {
		t.Fatalf("private Sheet spec history read reached the journal: %d", n)
	}
	// A failed audit releases nothing.
	mustExec(t, f.db, `CREATE TRIGGER deny_spec_history_audit BEFORE INSERT ON audit_logs WHEN NEW.user_id='wsadmin' AND NEW.entity_id='private-spec-book' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if w := versions(f.wsAdmin, "private-spec-book"); w.Code == 200 || strings.Contains(w.Body.String(), "secret-spec-name") {
		t.Fatalf("spec history released without its audit: %d %s", w.Code, w.Body.String())
	}
}
