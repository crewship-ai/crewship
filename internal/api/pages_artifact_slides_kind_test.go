package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactSlidesKindSelectedManifestAndStrictSnapshot(t *testing.T) {
	h, ws, owner, reader, page := slidesAPIFixture(t)
	listKind := func(user, want string) {
		t.Helper()
		rows := pagesListRows(t, h, ws, user, "MEMBER")
		if rows["three-screens"]["artifact_kind"] != want {
			t.Errorf("list %s: %v want %s", user, rows["three-screens"]["artifact_kind"], want)
		}
		r := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, user, "MEMBER", "")
		r.SetPathValue("slug", "three-screens")
		w := httptest.NewRecorder()
		h.Get(w, r)
		var detail map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["artifact_kind"] != want {
			t.Errorf("detail %s: %d %s want %s", user, w.Code, w.Body, want)
		}
	}
	snapshot := func(user, version, want string) {
		t.Helper()
		r := slidesAPIRequest(t, h, ws, user, page, "?version="+version)
		w := httptest.NewRecorder()
		h.GetArtifactProject(w, r)
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("snapshot %d %s", w.Code, w.Body)
		}
		got, _ := body["artifact_kind"].(string)
		if got != want {
			t.Errorf("snapshot %s/%s kind %q want %q", user, version, got, want)
		}
	}
	listKind(owner, "slides")
	listKind(reader, "slides")
	snapshot(owner, "1", "slides")
	snapshot(reader, "1", "slides")
	// A marker is insufficient: light metadata checks the closed selected index;
	// the full snapshot additionally validates the actual authored HTML profile.
	result, err := h.db.Exec(`UPDATE artifact_version_blobs SET content=replace(CAST(content AS TEXT),'<section','<script') WHERE page_id=? AND hash=json_extract((SELECT manifest_json FROM artifact_versions WHERE page_id=? AND version=2),'$.files."slides/intro.html".hash')`, page, page)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("invalid HTML mutation count %d", rows)
	}
	snapshot(owner, "2", "")
	snapshot(reader, "1", "slides")
	// Publish a plain Project privately: the reader must retain its shared type.
	body := artifactProjectBody(2, "Private Project")
	r := pagesRequest(t, "POST", "/api/v1/pages/three-screens/artifact/versions", ws, owner, "OWNER", body)
	r.SetPathValue("slug", "three-screens")
	// Use the fixture's authorized CLI token to avoid a weaker synthetic auth path.
	authorized := slidesAPIRequest(t, h, ws, owner, page, "versions")
	r.Header = authorized.Header
	r = r.WithContext(authorized.Context())
	r.Body = httptest.NewRequest("POST", "/", strings.NewReader(body)).Body
	w := httptest.NewRecorder()
	h.PublishArtifactProject(w, r)
	if w.Code != 200 {
		t.Fatalf("private project %d %s", w.Code, w.Body)
	}
	listKind(owner, "project")
	listKind(reader, "slides")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "")
	listKind(reader, "project")
	snapshot(reader, "1", "")
}

func TestArtifactSlidesFlagPreservesLargeDashboardClassification(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	var body map[string]any
	if err := json.Unmarshal([]byte(dashboardBody(0, "sales")), &body); err != nil {
		t.Fatal(err)
	}
	files := body["files"].([]any)
	for len(files) < 102 {
		n := len(files)
		files = append(files, map[string]any{"path": fmt.Sprintf("assets/asset-%03d.html", n), "media_type": "text/html", "content": fmt.Sprintf(`<p id="asset-%03d">Asset</p>`, n)})
	}
	body["files"] = files
	encoded, _ := json.Marshal(body)
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Large Dashboard",`+strings.TrimPrefix(string(encoded), "{"))
	if created.Code != 201 {
		t.Fatalf("valid102file dashboard creation: %d %s", created.Code, created.Body)
	}
	check := func(want string) {
		t.Helper()
		row := pagesListRows(t, h, ws, owner, "OWNER")["three-screens"]
		if row["artifact_kind"] != want {
			t.Errorf("list type %v want %s", row["artifact_kind"], want)
		}
		r := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, owner, "OWNER", "")
		r.SetPathValue("slug", "three-screens")
		w := httptest.NewRecorder()
		h.Get(w, r)
		var detail map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["artifact_kind"] != want {
			t.Errorf("detail type: %d %s want %s", w.Code, w.Body, want)
		}
	}
	check("dashboard")
	// A reserved deck marker still makes this ambiguous, irrespective of the
	// ordinary Project file allowance; it must never fall back to Dashboard.
	body["base_version"] = 1
	body["files"] = append(files, map[string]any{"path": "deck.json", "media_type": "application/json", "content": `{"schema_version":1,"order":["intro"]}`})
	encoded, _ = json.Marshal(body)
	if published := call(owner, "POST", "three-screens/artifact/versions", string(encoded)); published.Code != 200 {
		t.Fatalf("ambiguous fixture: %d %s", published.Code, published.Body)
	}
	check("project")
}
