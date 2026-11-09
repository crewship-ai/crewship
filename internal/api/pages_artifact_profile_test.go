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
}

// The catalog's kind facet uses the same derivation: an interactive app is not a dashboard.
func TestArtifactProfileCatalogKindFacet(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	for slug, files := range map[string]map[string]string{
		"sales":    {"dashboard.json": `{"schema_version":1,"collections":["sales"]}`},
		"showcase": {"dashboard.json": `{"schema_version":1,"collections":["showcase"]}`, "interactive.json": `{"schema_version":1}`},
	} {
		if w := call(owner, "POST", "artifacts", `{"slug":"`+slug+`","name":"`+slug+`",`+strings.TrimPrefix(projectWithFiles(t, 0, files), "{")); w.Code != 201 {
			t.Fatal(slug, w.Code, w.Body)
		}
	}
	slugs := func(kind string) []string {
		req := pagesRequest(t, "GET", "/api/v1/pages?kind="+kind+"&limit=50", ws, owner, "OWNER", "")
		w := httptest.NewRecorder()
		h.List(w, req)
		if w.Code != 200 {
			t.Fatalf("list %s: %d %s", kind, w.Code, w.Body)
		}
		var body struct {
			Items []struct {
				Slug    string `json:"slug"`
				Profile string `json:"artifact_profile"`
			} `json:"items"`
		}
		var rows []struct {
			Slug string `json:"slug"`
		}
		out := []string{}
		if json.Unmarshal(w.Body.Bytes(), &body) == nil && body.Items != nil {
			for _, item := range body.Items {
				out = append(out, item.Slug)
			}
			return out
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("list body %s", w.Body)
		}
		for _, row := range rows {
			out = append(out, row.Slug)
		}
		return out
	}
	if got := slugs("dashboard"); len(got) != 1 || got[0] != "sales" {
		t.Errorf("kind=dashboard = %v, want [sales]", got)
	}
	if got := slugs("project"); len(got) != 1 || got[0] != "showcase" {
		t.Errorf("kind=project = %v, want [showcase]", got)
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
