package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDocumentAvailabilityNativeSheetRequiresItsOwnLiveQualifiedProfile(t *testing.T) {
	for _, mode := range []string{"off", "unqualified", "word-only", "grid-only", "both"} {
		t.Run(mode, func(t *testing.T) {
			human := &craftNativeHTTP{enabled: mode != "off"}
			documents := newCraftNativeDocumentsHTTP(human, nil)
			if mode == "word-only" || mode == "both" {
				documents.applications["wordcraft"] = craftNativeApplication{}
			}
			if mode == "grid-only" || mode == "both" || mode == "off" {
				documents.applications["gridcraft"] = craftNativeApplication{}
			}
			router := &Router{pages: &PageHandler{}, craftNativeHTTP: human, craftNativeDocuments: documents}
			raw, err := json.Marshal(router.documentAvailability(httptest.NewRequest("GET", "/api/v1/documents/status", nil)))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["gridcraft_native"] != (mode == "grid-only" || mode == "both") {
				t.Fatalf("Sheet capability mode %s: %s", mode, raw)
			}
			if fields["wordcraft_native"] != (mode == "word-only" || mode == "both") {
				t.Fatalf("Word capability mode %s: %s", mode, raw)
			}
			if fields["enabled"] != false {
				t.Fatal("native capability enabled unrelated legacy Docs")
			}
		})
	}
}

func TestNativeSheetPageMetadataKeepsExplicitApplicationOnReopen(t *testing.T) {
	f, manager, ctx := craftNativeHTTPFixture(t)
	f.seedPage(t, "native-sheet", "native-sheet", "Native Sheet", f.userID, "")
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT 'native-sheet',workspace_id,'gridcraft',engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='native-page'`)
	for _, operation := range []string{"get", "list"} {
		t.Run(operation, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/api/v1/pages/native-sheet", nil).WithContext(ctx)
			request.SetPathValue("slug", "native-sheet")
			out := httptest.NewRecorder()
			if operation == "get" {
				manager.pages.Get(out, request)
			} else {
				manager.pages.List(out, request)
			}
			if out.Code != 200 {
				t.Fatalf("metadata status %d", out.Code)
			}
			var row map[string]any
			if operation == "get" {
				if err := json.Unmarshal(out.Body.Bytes(), &row); err != nil {
					t.Fatal(err)
				}
			} else {
				var rows []map[string]any
				if err := json.Unmarshal(out.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				for _, candidate := range rows {
					if candidate["id"] == "native-sheet" {
						row = candidate
					}
				}
			}
			if row["native_craft_application"] != "gridcraft" {
				t.Fatalf("Sheet reopen descriptor missing: %s", out.Body.String())
			}
		})
	}
}
