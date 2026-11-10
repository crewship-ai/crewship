package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// projectWithFiles is artifactProjectBody plus extra files at the given base version.
func projectWithFiles(t *testing.T, base int, extra map[string]string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(artifactProjectBody(base, "Profile")), &body); err != nil {
		t.Fatal(err)
	}
	files := body["files"].([]any)
	for path, content := range extra {
		files = append(files, map[string]any{"path": path, "media_type": "application/json", "content": content})
	}
	body["files"] = files
	raw, _ := json.Marshal(body)
	return string(raw)
}

// The catalog and the Page detail derive one kind and one profile from the
// selected version's manifest (#5152): an interactive project is an App even
// when it declares data collections, and a design system says so before
// anyone opens it. A reader sees the shared version's profile, never the
// owner's private latest.
func TestArtifactProfileFromSelectedManifest(t *testing.T) {
	const (
		dashboard   = `{"schema_version":1,"collections":["showcase"]}`
		interactive = `{"schema_version":1}`
		designSys   = `{"schema_version":1,"name":"Tokens","tokens":{}}`
	)
	cases := []struct {
		name        string
		files       map[string]string
		slides      bool
		wantKind    string
		wantProfile string
	}{
		{name: "plain project", wantKind: "project"},
		{name: "data dashboard stays a dashboard", files: map[string]string{"dashboard.json": dashboard}, wantKind: "dashboard"},
		{name: "interactive app with a helper collection is an app", files: map[string]string{"dashboard.json": dashboard, "interactive.json": interactive}, wantKind: "project", wantProfile: "interactive"},
		{name: "interactive app with slides switched on", files: map[string]string{"dashboard.json": dashboard, "interactive.json": interactive}, slides: true, wantKind: "project", wantProfile: "interactive"},
		{name: "design system", files: map[string]string{"design-system.json": designSys}, wantKind: "project", wantProfile: "design_system"},
		{name: "design system with slides switched on", files: map[string]string{"design-system.json": designSys}, slides: true, wantKind: "project", wantProfile: "design_system"},
		{name: "design system with a data collection is a library", files: map[string]string{"design-system.json": designSys, "dashboard.json": dashboard}, wantKind: "project", wantProfile: "design_system"},
		{name: "design system wins over a runtime marker", files: map[string]string{"design-system.json": designSys, "interactive.json": interactive}, wantKind: "project", wantProfile: "design_system"},
		{name: "a deck file decides nothing while Slides is off", files: map[string]string{"deck.json": `{"schema_version":1,"order":["intro"]}`, "dashboard.json": dashboard, "interactive.json": interactive}, wantKind: "project", wantProfile: "interactive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, owner, call := artifactProjectFixture(t)
			t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
			if tc.slides {
				t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
			}
			create := `{"slug":"three-screens","name":"Profiled",` + strings.TrimPrefix(projectWithFiles(t, 0, tc.files), "{")
			if w := call(owner, "POST", "artifacts", create); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			assertArtifactProfile(t, h, ws, owner, tc.wantKind, tc.wantProfile)
		})
	}
}

func TestArtifactProfileReaderSeesSharedVersion(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	create := `{"slug":"three-screens","name":"Profiled",` + strings.TrimPrefix(projectWithFiles(t, 0, map[string]string{"dashboard.json": `{"schema_version":1,"collections":["sales"]}`}), "{")
	if w := call(owner, "POST", "artifacts", create); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	reader := "profile-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'profile-reader@example.test')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT id,'user',?,'read',?,datetime('now') FROM pages WHERE slug='three-screens'`, reader, owner)
	next := projectWithFiles(t, 1, map[string]string{"dashboard.json": `{"schema_version":1,"collections":["sales"]}`, "interactive.json": `{"schema_version":1}`})
	if w := call(owner, "POST", "three-screens/artifact/versions", next); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertArtifactProfile(t, h, ws, owner, "project", "interactive")
	assertArtifactProfile(t, h, ws, reader, "dashboard", "")
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":2,"version":2}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertArtifactProfile(t, h, ws, reader, "project", "interactive")
	// An audience with no selected shared version learns no private latest
	// declaration, even while its Page-level read grant remains live.
	mustExec(t, h.db, `UPDATE artifact_version_heads SET shared_version=0 WHERE workspace_id=?`, ws)
	assertArtifactProfile(t, h, ws, owner, "project", "interactive")
	assertArtifactProfile(t, h, ws, reader, "project", "")
	// Revocation removes metadata from both catalog and detail, not only the
	// runtime/content endpoint.
	mustExec(t, h.db, `DELETE FROM page_grants WHERE subject_type='user' AND subject_id=?`, reader)
	if _, visible := pagesListRows(t, h, ws, reader, "MEMBER")["three-screens"]; visible {
		t.Fatal("revoked reader still sees catalog profile")
	}
	req := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, reader, "MEMBER", "")
	req.SetPathValue("slug", "three-screens")
	denied := httptest.NewRecorder()
	h.Get(denied, req)
	if denied.Code != 404 || strings.Contains(denied.Body.String(), "interactive") {
		t.Fatalf("revoked profile read: %d %s", denied.Code, denied.Body.String())
	}
}

// The catalog's kind facet and kind sort use the same derivation: an
// interactive app is not a dashboard, and a design system is a design (#5152).
func TestArtifactProfileCatalogKindFacet(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	for slug, files := range map[string]map[string]string{
		"sales":    {"dashboard.json": `{"schema_version":1,"collections":["sales"]}`},
		"showcase": {"dashboard.json": `{"schema_version":1,"collections":["showcase"]}`, "interactive.json": `{"schema_version":1}`},
		"tokens":   {"design-system.json": `{"schema_version":1,"name":"Tokens","tokens":{}}`, "dashboard.json": `{"schema_version":1,"collections":["swatches"]}`},
	} {
		if w := call(owner, "POST", "artifacts", `{"slug":"`+slug+`","name":"`+slug+`",`+strings.TrimPrefix(projectWithFiles(t, 0, files), "{")); w.Code != 201 {
			t.Fatal(slug, w.Code, w.Body)
		}
	}
	slugs := func(query string) []string {
		req := pagesRequest(t, "GET", "/api/v1/pages?"+query, ws, owner, "OWNER", "")
		w := httptest.NewRecorder()
		h.List(w, req)
		var rows []struct {
			Slug string `json:"slug"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil {
			t.Fatalf("list %s: %d %s", query, w.Code, w.Body)
		}
		out := []string{}
		for _, row := range rows {
			out = append(out, row.Slug)
		}
		return out
	}
	for query, want := range map[string]string{
		"kind=dashboard": "sales",
		"kind=project":   "showcase",
		"kind=design":    "tokens",
		// LIBRARY_KINDS order: project, dashboard, …, design.
		"kind=project,dashboard,design&sort=kind": "showcase,sales,tokens",
	} {
		if got := strings.Join(slugs(query), ","); got != want {
			t.Errorf("%s = %s, want %s", query, got, want)
		}
	}
}

func assertArtifactProfile(t *testing.T, h *PageHandler, ws, user, kind, profile string) {
	t.Helper()
	row := pagesListRows(t, h, ws, user, "MEMBER")["three-screens"]
	if row["artifact_kind"] != kind || stringOrEmpty(row["artifact_profile"]) != profile {
		t.Errorf("%s list kind=%v profile=%v, want %s %q", user, row["artifact_kind"], row["artifact_profile"], kind, profile)
	}
	r := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, user, "MEMBER", "")
	r.SetPathValue("slug", "three-screens")
	w := httptest.NewRecorder()
	h.Get(w, r)
	var detail map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["artifact_kind"] != kind || stringOrEmpty(detail["artifact_profile"]) != profile {
		t.Errorf("%s detail=%d %s, want %s %q", user, w.Code, w.Body, kind, profile)
	}
}

func stringOrEmpty(value any) string {
	text, _ := value.(string)
	return text
}

// Exercise the SQL projection itself, including corrupt/oversized optional
// metadata. UTF-8 byte length, not rune count, bounds the JSON parser.
func TestArtifactProfileSQLManifestBounds(t *testing.T) {
	h, _, _, _ := artifactProjectFixture(t)
	for _, tc := range []struct {
		name     string
		manifest any
		want     string
	}{
		{"absent selected version", nil, ""},
		{"invalid JSON", "{invalid", ""},
		{"unknown marker", `{"files":{"unrelated.json":{}}}`, ""},
		{"interactive marker", `{"files":{"interactive.json":{}}}`, "interactive"},
		{"design system precedence", `{"files":{"interactive.json":{},"design-system.json":{}}}`, "design_system"},
		{"oversize ASCII", `{"files":{"interactive.json":{}},"padding":"` + strings.Repeat("x", 128<<10) + `"}`, ""},
		{"oversize UTF8", `{"files":{"interactive.json":{}},"padding":"` + strings.Repeat("ě", 70<<10) + `"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			err := h.db.QueryRow("SELECT "+artifactProfileSQL("v", "'artifact_project'")+" FROM (SELECT ? AS manifest_json) v", false, tc.manifest).Scan(&got)
			if err != nil || got != tc.want {
				t.Fatalf("profile=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
}
