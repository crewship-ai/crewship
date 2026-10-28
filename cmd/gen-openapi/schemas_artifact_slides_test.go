package main

import (
	"encoding/json"
	"testing"
)

func TestArtifactSlidesMetadataEnumsMatchSelectedPageWire(t *testing.T) {
	raw, err := json.Marshal(buildDocument([]route{{method: "GET", path: "/api/v1/pages"}, {method: "GET", path: "/api/v1/pages/{slug}"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pointer := range []string{
		"/paths/~1api~1v1~1pages/get/responses/200/content/application~1json/schema/items/properties/artifact_kind",
		"/paths/~1api~1v1~1pages~1{slug}/get/responses/200/content/application~1json/schema/properties/artifact_kind",
		"/components/schemas/ArtifactProjectSnapshot/properties/artifact_kind",
	} {
		schema := compileAt(t, raw, pointer)
		if err := schema.Validate("slides"); err != nil {
			t.Fatalf("selected Slides rejected by %s: %v", pointer, err)
		}
		if schema.Validate("unsupported") == nil {
			t.Fatalf("unpublished metadata kind accepted by %s", pointer)
		}
	}
}
