package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestPageContentDescriptorStoredSchemaAndList(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	doc := pagesCreate(t, h, ws, user, "registry-doc")
	id := doc["id"].(string)
	execOrFatal(t, h.db, `DELETE FROM page_panels WHERE page_id=?`, id)
	execOrFatal(t, h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,1,X'01','{}')`, ws, id)
	assert := func(row map[string]any) {
		t.Helper()
		descriptor, ok := row["content_descriptor"].(map[string]any)
		if !ok || descriptor["type_id"] != "doc" || descriptor["schema_version"] != float64(1) || descriptor["supported"] != true {
			t.Fatalf("stored schema 1 lost: %#v", row)
		}
	}
	assert(pagesGet(t, h, ws, user, "OWNER", "registry-doc"))
	request := pagesRequest(t, "GET", "/api/v1/pages", ws, user, "OWNER", "")
	response := httptest.NewRecorder()
	h.List(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["id"] == id {
			assert(row)
			return
		}
	}
	t.Fatal("document missing from list")
}
func TestPageContentDescriptorStoredProfiles(t *testing.T) {
	for _, test := range []struct {
		name      string
		record    pageRecord
		panels    int
		kind      string
		supported bool
	}{
		{"legacy HTML", pageRecord{HasArtifact: true}, 0, "html", true},
		{"unsupported HTML", pageRecord{HasArtifact: true, ArtifactManifest: `{"format_version":1,"runtime_profile":"interactive-html-v1"}`}, 0, "html", false},
		{"invalid HTML manifest", pageRecord{HasArtifact: true, ArtifactManifest: `{`}, 0, "html", false},
		{"draft", pageRecord{HasProject: true}, 0, "panels", true},
		{"empty", pageRecord{OwnerUserID: "human"}, 0, "panels", true},
		{"collision", pageRecord{HasDocument: true, DocumentSchema: 2, HasProject: true}, 0, "conflicting", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := derivePageContent(&test.record, test.panels)
			if actual.ContentDescriptor.TypeID != test.kind || actual.ContentDescriptor.Supported != test.supported {
				t.Fatalf("%#v", actual)
			}
			if test.record.HasProject && test.supported && actual.ApplicationDescriptor == nil {
				t.Fatal("draft source hidden")
			}
			if actual.CanInitializeContent != (test.name == "empty") {
				t.Fatalf("initialize eligibility: %#v", actual)
			}
		})
	}
}
