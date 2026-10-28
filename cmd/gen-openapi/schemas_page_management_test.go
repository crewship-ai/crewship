package main

import (
	"encoding/json"
	"testing"
)

func TestPageArchiveContractRejectsMutationOutsideReviewedCatalogFields(t *testing.T) {
	request := pagesSchemaCatalog()["POST /api/v1/pages/{slug}/archive"].Request
	doc := buildDocument(nil)
	doc["components"].(map[string]any)["schemas"].(map[string]any)["ArchiveRequest"] = request
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	schema := compileAt(t, raw, "/components/schemas/ArchiveRequest")
	for _, body := range []map[string]any{
		{"dry_run": true, "pages_version": float64(0)},
		{"confirm": true, "review_token": "server-review", "pages_version": float64(3)},
	} {
		if err := schema.Validate(body); err != nil {
			t.Fatalf("valid review rejected: %v", err)
		}
	}
	for _, body := range []map[string]any{
		{"dry_run": true},
		{"pages_version": float64(0)},
		{"confirm": true, "pages_version": float64(0)},
		{"dry_run": true, "confirm": true, "pages_version": float64(0)},
		{"dry_run": true, "pages_version": float64(-1)},
		{"dry_run": true, "pages_version": float64(1), "owner": "somebody-else"},
		{"confirm": true, "pages_version": float64(1), "delete_content": true},
	} {
		if schema.Validate(body) == nil {
			t.Fatalf("unsafe catalog contract accepted %#v", body)
		}
	}
}

func TestPageTestMarkerIsCreationOnlyInPublicContract(t *testing.T) {
	catalog := pagesSchemaCatalog()
	create := catalog["POST /api/v1/pages"].Request["properties"].(map[string]any)
	if create["created_by_test"] == nil {
		t.Fatal("human creation cannot explicitly mark QA items")
	}
	patch := catalog["PATCH /api/v1/pages/{slug}"].Request["properties"].(map[string]any)
	if patch["created_by_test"] != nil || patch["archived"] != nil {
		t.Fatal("metadata patch bypasses creation marker or reviewed archive")
	}
}
