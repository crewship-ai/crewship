package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// RBAC matrix for versioned artifacts at the real API boundary (#4884,
// Security PRD 2026-11-04 §4 gaps 7 and 8; SEC-RBAC-01..03, SEC-VER-01,
// SEC-AUD-01/02). Every request goes through NewRouter: real CLI-token
// authentication, workspace middleware, route role gates and handlers over a
// real SQLite database. No permission helper is replaced.
//
// Model (verified in pages_artifact_project.go, pages_artifact_data.go,
// pages_artifact_comment_authority.go, pages_private_audience.go, #4839):
//   - the Page owner reads, shares and restores every version;
//   - an Edit grantee reads and publishes only the latest version and never
//     shares (artifactProjectAuthority with share=true refuses it) nor
//     restores (artifactProjectOwnerAuthority);
//   - a Read or Comment grantee reads only the shared pin; Comment adds
//     feedback on that pin;
//   - a live, trusted workspace OWNER/ADMIN reads every version and the full
//     history, one audit row per request, never write/share/restore;
//   - private output (never shared) admits only its custodian and the
//     audited OWNER/ADMIN read; grants never widen it;
//   - a restricted (Guest) membership never reaches these routes.

const (
	rbacWS = "ws-old"
	rbacQ  = "?workspace_id=" + rbacWS
)

type rbacMatrix struct {
	f      *instanceFixture
	tokens map[string]string
	page   string // page id of "matrix"
}

// rbacProjectBody is a dashboard-capable Artifact Project version whose hero
// carries a per-version marker, so any leaked version is visible in a body.
func rbacProjectBody(base, version int) string {
	return strings.Replace(dashboardBody(base, "sales"), "Dashboard", fmt.Sprintf("SECRET-V%d", version), 1)
}

func rbacMarkers(body string) string {
	out := ""
	for v := 1; v <= 9; v++ {
		if strings.Contains(body, fmt.Sprintf("SECRET-V%d", v)) {
			out += fmt.Sprint(v)
		}
	}
	return out
}

func (m *rbacMatrix) do(user, method, path, body string) *httptest.ResponseRecorder {
	m.f.t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return m.f.do(m.tokens[user], method, path+sep+"workspace_id="+rbacWS, body)
}

// rbacUserID maps a persona to its user id; the instance fixture names the
// workspace OWNER "boss".
func rbacUserID(persona string) string {
	if persona == "wsowner" {
		return "boss"
	}
	return persona
}

func (m *rbacMatrix) audits(user string) int {
	m.f.t.Helper()
	user = rbacUserID(user)
	var n int
	if err := m.f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE user_id=? AND action IN (?,?)`, user, auditActionPrivateArtifactRead, auditActionWorkspaceAdminArtifactRead).Scan(&n); err != nil {
		m.f.t.Fatal(err)
	}
	return n
}

// newRBACMatrix seeds one workspace with every persona and one Artifact
// Project "matrix" owned by the MEMBER author. shared=true: v1, v2, share v2,
// v3 (latest 3, pin 2: one version older and one newer than the pin).
// shared=false: v1, v2 and never shared, so the Page keeps its private
// audience. Each grantee holds its grant in both states.
func newRBACMatrix(t *testing.T, shared bool) *rbacMatrix {
	t.Helper()
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	f := gridcraftInstanceFixture(t)
	m := &rbacMatrix{f: f, tokens: map[string]string{"wsowner": f.boss, "wsadmin": f.wsAdmin}}
	for _, u := range []struct{ id, role string }{
		{"author", "MANAGER"}, // demoted to MEMBER below; MEMBER cannot create
		{"member", "MEMBER"},
		{"reader", "MEMBER"},
		{"commenter", "MEMBER"},
		{"editor", "MEMBER"},
		{"guest", "MEMBER"},
		{"guestadmin", "ADMIN"},
	} {
		seedInstanceUser(t, f.db, u.id, u.id+"@ex.com", rbacWS, u.role)
		m.tokens[u.id] = mintTokenFor(t, f.db, u.id, "rbacmatrix"+u.id+"0000000000000")
	}
	// guestadmin is a legacy restricted ADMIN row: migration
	// 20261014190000_guest_administrator_invariant refuses new conflicts but
	// leaves existing rows intact, so the read path must still suppress the
	// administrator read for one (SEC-RBAC-02). Recreate such a row here.
	mustExec(t, f.db, `DROP TRIGGER guest_administrator_update`)
	mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id IN ('guest','guestadmin')`)
	if w := m.do("author", "POST", "/api/v1/pages/artifacts", `{"slug":"matrix","name":"Matrix",`+strings.TrimPrefix(rbacProjectBody(0, 1), "{")); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	if w := m.do("author", "POST", "/api/v1/pages/matrix/artifact/versions", rbacProjectBody(1, 2)); w.Code != 200 {
		t.Fatalf("v2 %d %s", w.Code, w.Body)
	}
	if shared {
		if w := m.do("author", "POST", "/api/v1/pages/matrix/artifact/share", `{"base_version":2,"version":2}`); w.Code != 200 {
			t.Fatalf("share %d %s", w.Code, w.Body)
		}
		if w := m.do("author", "POST", "/api/v1/pages/matrix/artifact/versions", rbacProjectBody(2, 3)); w.Code != 200 {
			t.Fatalf("v3 %d %s", w.Code, w.Body)
		}
	}
	latest := 2
	if shared {
		latest = 3
	}
	if w := m.do("author", "POST", "/api/v1/pages/matrix/artifact/data/sales", fmt.Sprintf(`{"base_version":%d,"documents":[{"id":"row","ifVersion":0,"value":{"amount":4242}}]}`, latest)); w.Code != 200 {
		t.Fatalf("data %d %s", w.Code, w.Body)
	}
	if err := f.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='matrix'`, rbacWS).Scan(&m.page); err != nil {
		t.Fatal(err)
	}
	// Grants are seeded directly: on a private Page the custodian's grant API
	// is itself part of what is under test, and a grant row is exactly what
	// the API would have written for the shared case.
	for user, level := range map[string]string{"reader": "read", "commenter": "comment", "editor": "write", "guest": "read", "guestadmin": "read"} {
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,?,'author',datetime('now'))`, m.page, user, level)
	}
	mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='author'`)
	var private bool
	if err := f.db.QueryRow(`SELECT private_audience FROM pages WHERE id=?`, m.page).Scan(&private); err != nil || private == shared {
		t.Fatalf("fixture audience private=%v shared=%v err=%v", private, shared, err)
	}
	return m
}

// rbacWant is one cell: status, the version markers the body may carry (and
// must carry, when set), administrator audit rows written by this request and,
// for the history surface, the number of versions listed.
type rbacWant struct {
	code    int
	marks   string
	audits  int
	history int // -1: not checked
}

func w(code int, marks string, audits int) rbacWant { return rbacWant{code, marks, audits, -1} }

type rbacSurface struct {
	name   string
	method string
	path   string
	body   string
}

// Read surfaces, identical for both audience states. "newer" is v3 on the
// shared Project (newer than the pin); on the private Project it does not
// exist and every reader gets the same 404 as for an existing version.
var rbacReadSurfaces = []rbacSurface{
	{"current", "GET", "/api/v1/pages/matrix/artifact", ""},
	{"pin-v2", "GET", "/api/v1/pages/matrix/artifact?version=2", ""},
	{"older-v1", "GET", "/api/v1/pages/matrix/artifact?version=1", ""},
	{"newer-v3", "GET", "/api/v1/pages/matrix/artifact?version=3", ""},
	{"absent-v99", "GET", "/api/v1/pages/matrix/artifact?version=99", ""},
	{"history", "GET", "/api/v1/pages/matrix/artifact/versions", ""},
	{"comments", "GET", "/api/v1/pages/matrix/artifact/comments", ""},
	{"export", "GET", "/api/v1/pages/matrix/export", ""},
	{"data-pin-v2", "GET", "/api/v1/pages/matrix/artifact/data/sales?version=2", ""},
	{"data-older-v1", "GET", "/api/v1/pages/matrix/artifact/data/sales?version=1", ""},
	{"data-absent-v99", "GET", "/api/v1/pages/matrix/artifact/data/sales?version=99", ""},
}

func rbacComment(version int) string {
	return fmt.Sprintf(`{"version":%d,"path":"index.html","selector":"#hero","quote":"SECRET-V%d","text":"matrix feedback"}`, version, version)
}

// Mutating surfaces, run after the reads in the order listed.
func rbacActionSurfaces(latest, pin int) []rbacSurface {
	return []rbacSurface{
		{"comment-create", "POST", "/api/v1/pages/matrix/artifact/comments", rbacComment(pin)},
		{"comment-latest", "POST", "/api/v1/pages/matrix/artifact/comments", rbacComment(latest)},
		{"grant-put", "PUT", "/api/v1/pages/matrix/grants", `{"subject_type":"user","subject_id":"member","level":"read"}`},
		{"share", "POST", "/api/v1/pages/matrix/artifact/share", fmt.Sprintf(`{"base_version":%d,"version":%d}`, latest, latest)},
		{"publish", "POST", "/api/v1/pages/matrix/artifact/versions", rbacProjectBody(latest, 7)},
		// Based on the head an allowed publish just wrote; every caller that
		// may not restore is refused before the version check.
		{"restore", "POST", "/api/v1/pages/matrix/artifact/restore", fmt.Sprintf(`{"base_version":%d,"version":1}`, latest+1)},
	}
}

func runRBACRow(t *testing.T, m *rbacMatrix, user string, surfaces []rbacSurface, want map[string]rbacWant) {
	t.Helper()
	for _, s := range surfaces {
		exp, ok := want[s.name]
		if !ok {
			t.Fatalf("no expectation for %s/%s", user, s.name)
		}
		before := m.audits(user)
		got := m.do(user, s.method, s.path, s.body)
		after := m.audits(user)
		body := got.Body.String()
		if got.Code != exp.code {
			t.Errorf("%s %s: HTTP %d want %d: %.200s", user, s.name, got.Code, exp.code, body)
		}
		if marks := rbacMarkers(body); s.method == "GET" && marks != exp.marks {
			t.Errorf("%s %s: body carries versions %q want %q", user, s.name, marks, exp.marks)
		}
		if strings.HasPrefix(s.name, "data") && exp.code != 200 && strings.Contains(body, "4242") {
			t.Errorf("%s %s: refused data read released rows: %.200s", user, s.name, body)
		}
		if exp.audits == atLeastOne && after-before < 1 || exp.audits != atLeastOne && after-before != exp.audits {
			t.Errorf("%s %s: administrator audit rows %d want %d", user, s.name, after-before, exp.audits)
		}
		if exp.history >= 0 && got.Code == 200 {
			var view struct {
				Versions []json.RawMessage `json:"versions"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil || len(view.Versions) != exp.history {
				t.Errorf("%s %s: history lists %d versions want %d (%v)", user, s.name, len(view.Versions), exp.history, err)
			}
		}
	}
}

// atLeastOne marks an administrator mutation whose handler runs the read
// authority in more than one fenced transaction (see the comment-create
// interpretation): it must be audited, the exact row count is not the
// contract.
const atLeastOne = -1

func withHistory(x rbacWant, n int) rbacWant { x.history = n; return x }

// SHARED Project: latest v3, pin v2.
//
// Interpretations (written down where the PRD and code needed reading):
//   - SEC-VER-01 asks for "404 without existence signal" for any other
//     version. The Project read does exactly that: v1, v3 and the absent v99
//     are the same 404 for every grantee. Dashboard data selects the version
//     inside the store transaction and answers 409 artifact_version_conflict
//     for any version the caller is not entitled to; v1 and the absent v99
//     answer identically, so the 409 carries no existence signal and no rows.
//     The matrix pins that today's code is a uniform refusal, not 404.
//   - An editor reads the latest only; the shared pin by number is outside
//     the editor's mandate (artifactVersionCoveredWithoutAdmin) and is 404.
//   - Page export (/pages/{slug}/export) is the Page bundle (spec and panels).
//     It carries no Project version bytes; it needs Page edit authority, so a
//     read/comment grantee gets 403 (they already know the Page exists) and
//     an ungranted member 404. The administrator export is audited.
//   - A workspace OWNER/ADMIN may administer grants and comment on a
//     NON-private Page: that is the pre-existing workspace manage authority
//     (mayAdministerGrants, mayEditSpec -> canRole(role,"manage")), not the
//     #4839 read rule. Sharing a Project version, restore and publish stay
//     with the owner (and publish with an editor). Private output is the
//     boundary where the read rule must not become write (see the private
//     matrix below).
//   - "History" (/artifact/versions) lists every retained version only to
//     the owner and the administrator read; a grantee's list carries exactly
//     the one version it may read (the pin, or the latest for an editor).
//   - Administrator audit: one row per read request. Two deliberate
//     exceptions are pinned: a read of an absent version (v99) still writes
//     its row (the attempt is recorded, nothing is released), and an
//     ADMIN's feedback POST on a non-private Project runs the read authority
//     in several fenced transactions and writes more than one row (observed
//     4 for 201, 2 for 409); the matrix requires "at least one".
//   - A private Project refuses every non-custodian write, share, restore and
//     publish with the same 404 as an unknown Page; grant administration on
//     it answers 409 for the custodian audience (grants cannot widen private
//     output) and 404 for an ADMIN.
//   - An editor never changes sharing: /artifact/share is 403 and so is grant
//     administration (owner or manage role only).
func TestArtifactRBACMatrixSharedProject(t *testing.T) {
	const latest, pin = 3, 2
	none := w(404, "", 0)
	admin := map[string]rbacWant{
		"current": withHistory(w(200, "3", 1), 3), "pin-v2": w(200, "2", 1), "older-v1": w(200, "1", 1), "newer-v3": w(200, "3", 1),
		"absent-v99": w(404, "", 0), "history": withHistory(w(200, "3", 1), 3), "comments": w(200, "3", 1), "export": w(200, "", 1),
		"data-pin-v2": w(200, "", 1), "data-older-v1": w(200, "", 1), "data-absent-v99": w(404, "", 1),
		"comment-create": w(201, "", atLeastOne), "comment-latest": w(409, "", atLeastOne), "grant-put": w(200, "", 0),
		"share": w(403, "", 0), "restore": w(403, "", 0), "publish": w(403, "", 0),
	}
	grantee := func(comment int) map[string]rbacWant {
		return map[string]rbacWant{
			"current": withHistory(w(200, "2", 0), 1), "pin-v2": w(200, "2", 0), "older-v1": none, "newer-v3": none,
			"absent-v99": none, "history": withHistory(w(200, "2", 0), 1), "comments": w(200, "2", 0), "export": w(403, "", 0),
			"data-pin-v2": w(200, "", 0), "data-older-v1": w(409, "", 0), "data-absent-v99": w(409, "", 0),
			"comment-create": w(comment, "", 0), "comment-latest": w(403, "", 0), "grant-put": w(403, "", 0),
			"share": w(403, "", 0), "restore": w(403, "", 0), "publish": w(403, "", 0),
		}
	}
	reader, commenter := grantee(403), grantee(201)
	commenter["comment-latest"] = w(409, "", 0) // feedback authority, but only on the pin
	notMember := map[string]rbacWant{}
	for _, s := range append(append([]rbacSurface{}, rbacReadSurfaces...), rbacActionSurfaces(latest, pin)...) {
		notMember[s.name] = none
	}
	rows := map[string]map[string]rbacWant{
		"author": {
			"current": withHistory(w(200, "3", 0), 3), "pin-v2": w(200, "2", 0), "older-v1": w(200, "1", 0), "newer-v3": w(200, "3", 0),
			"absent-v99": none, "history": withHistory(w(200, "3", 0), 3), "comments": w(200, "3", 0), "export": w(200, "", 0),
			"data-pin-v2": w(200, "", 0), "data-older-v1": w(200, "", 0), "data-absent-v99": w(404, "", 0),
			"comment-create": w(409, "", 0), "comment-latest": w(201, "", 0), "grant-put": w(200, "", 0),
			"share": w(200, "", 0), "restore": w(200, "", 0), "publish": w(200, "", 0),
		},
		"wsowner":   admin,
		"wsadmin":   admin,
		"member":    notMember,
		"reader":    reader,
		"commenter": commenter,
		"editor": {
			"current": withHistory(w(200, "3", 0), 1), "pin-v2": none, "older-v1": none, "newer-v3": w(200, "3", 0),
			"absent-v99": none, "history": withHistory(w(200, "3", 0), 1), "comments": w(200, "3", 0), "export": w(200, "", 0),
			"data-pin-v2": w(409, "", 0), "data-older-v1": w(409, "", 0), "data-absent-v99": w(409, "", 0),
			"comment-create": w(409, "", 0), "comment-latest": w(201, "", 0), "grant-put": w(403, "", 0),
			"share": w(403, "", 0), "restore": w(403, "", 0), "publish": w(200, "", 0),
		},
		"guest":      notMember,
		"guestadmin": notMember,
	}
	for user, want := range rows {
		t.Run(user, func(t *testing.T) {
			m := newRBACMatrix(t, true)
			runRBACRow(t, m, user, rbacReadSurfaces, want)
			runRBACRow(t, m, user, rbacActionSurfaces(latest, pin), want)
			var gotLatest, gotPin int
			if err := m.f.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?`, m.page).Scan(&gotLatest, &gotPin); err != nil {
				t.Fatal(err)
			}
			// Only the owner may move the pin (share v3, publish -> v4, restore -> v5);
			// an editor's publish adds v4 and leaves the pin.
			wantLatest, wantPin := latest, pin
			switch user {
			case "author":
				wantLatest, wantPin = latest+2, latest
			case "editor":
				wantLatest = latest + 1
			}
			if gotLatest != wantLatest || gotPin != wantPin {
				t.Errorf("%s left head latest=%d pin=%d want %d/%d", user, gotLatest, gotPin, wantLatest, wantPin)
			}
		})
	}
}

// PRIVATE Project (never shared): latest v2, no pin. Grants on a private
// Page never widen its audience (pages_artifact_project.go "Private audience
// remains a separate boundary even for an Edit grant"); only the custodian
// and the audited OWNER/ADMIN read. Write, share, restore, grant
// administration and feedback stay with the custodian and the workspace
// OWNER (privatePageCustodian) for Page-level administration; the Project
// share/restore/publish handlers admit only the owner.
func TestArtifactRBACMatrixPrivateProject(t *testing.T) {
	const latest, pin = 2, 2
	none := w(404, "", 0)
	notReader := map[string]rbacWant{}
	for _, s := range append(append([]rbacSurface{}, rbacReadSurfaces...), rbacActionSurfaces(latest, pin)...) {
		notReader[s.name] = none
	}
	admin := func(grantPut int) map[string]rbacWant {
		return map[string]rbacWant{
			"current": withHistory(w(200, "2", 1), 2), "pin-v2": w(200, "2", 1), "older-v1": w(200, "1", 1), "newer-v3": none,
			"absent-v99": none, "history": withHistory(w(200, "2", 1), 2), "comments": w(200, "2", 1), "export": w(200, "", 1),
			"data-pin-v2": w(200, "", 1), "data-older-v1": w(200, "", 1), "data-absent-v99": w(404, "", 1),
			"comment-create": w(403, "", 0), "comment-latest": w(403, "", 0), "grant-put": w(grantPut, "", 0),
			"share": none, "restore": none, "publish": none,
		}
	}
	rows := map[string]map[string]rbacWant{
		"author": {
			"current": withHistory(w(200, "2", 0), 2), "pin-v2": w(200, "2", 0), "older-v1": w(200, "1", 0), "newer-v3": none,
			"absent-v99": none, "history": withHistory(w(200, "2", 0), 2), "comments": w(200, "2", 0), "export": w(200, "", 0),
			"data-pin-v2": w(200, "", 0), "data-older-v1": w(200, "", 0), "data-absent-v99": none,
			"comment-create": w(201, "", 0), "comment-latest": w(201, "", 0), "grant-put": w(409, "", 0),
			"share": w(200, "", 0), "restore": w(200, "", 0), "publish": w(200, "", 0),
		},
		// The workspace OWNER is a Page-level custodian of private output for
		// grant administration (privatePageCustodian); the ADMIN is not.
		"wsowner":    admin(409),
		"wsadmin":    admin(404),
		"member":     notReader,
		"reader":     notReader,
		"commenter":  notReader,
		"editor":     notReader,
		"guest":      notReader,
		"guestadmin": notReader,
	}
	for user, want := range rows {
		t.Run(user, func(t *testing.T) {
			m := newRBACMatrix(t, false)
			runRBACRow(t, m, user, rbacReadSurfaces, want)
			runRBACRow(t, m, user, rbacActionSurfaces(latest, pin), want)
		})
	}
}
