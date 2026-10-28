package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactSlidesDeckIndexEntryPointMatchesProjectionAndMetadata(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	var body map[string]any
	if err := json.Unmarshal([]byte(slidesArtifactBody(0, "Žluťoučký kůň")), &body); err != nil {
		t.Fatal(err)
	}
	body["entrypoint"] = "deck.json"
	raw, _ := json.Marshal(body)
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Index-entry Slides",`+strings.TrimPrefix(string(raw), "{"))
	if created.Code != 201 {
		t.Fatalf("valid index-entry publish %d %s", created.Code, created.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	projection := httptest.NewRecorder()
	h.GetArtifactSlides(projection, slidesAPIRequest(t, h, ws, owner, page, "slides?version=1"))
	if projection.Code != 200 {
		t.Fatalf("full projection calibration %d %s", projection.Code, projection.Body)
	}
	snapshot := httptest.NewRecorder()
	h.GetArtifactProject(snapshot, slidesAPIRequest(t, h, ws, owner, page, "?version=1"))
	var snapshotBody map[string]any
	if snapshot.Code != 200 || json.Unmarshal(snapshot.Body.Bytes(), &snapshotBody) != nil || snapshotBody["artifact_kind"] != "slides" {
		t.Fatalf("snapshot calibration %d %s", snapshot.Code, snapshot.Body)
	}
	row := pagesListRows(t, h, ws, owner, "OWNER")["three-screens"]
	if row["artifact_kind"] != "slides" {
		t.Errorf("list mismatches full projection: %v", row["artifact_kind"])
	}
	r := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, owner, "OWNER", "")
	r.SetPathValue("slug", "three-screens")
	detail := httptest.NewRecorder()
	h.Get(detail, r)
	var detailBody map[string]any
	if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &detailBody) != nil || detailBody["artifact_kind"] != "slides" {
		t.Errorf("detail mismatches projection: %d %s", detail.Code, detail.Body)
	}
}
