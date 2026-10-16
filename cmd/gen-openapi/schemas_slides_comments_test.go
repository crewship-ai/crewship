package main

import "testing"

func TestSlideCommentsCatalogStrictHumanProtocol(t *testing.T) {
	catalog := slideCommentCatalog()
	if len(catalog) != 6 {
		t.Fatal("missing comments routes", len(catalog))
	}
	for _, path := range []string{"", "/{threadId}/replies", "/{threadId}/resolve"} {
		route := catalog["POST /api/v1/pages/{slug}/slides/comments"+path]
		if !route.RequestRequired || route.Request["additionalProperties"] != false {
			t.Fatal("non-strict comment envelope", path)
		}
		props := route.Request["properties"].(map[string]any)
		if props["page_id"] == nil || props["schema_version"] == nil || props["mutation_id"] == nil {
			t.Fatal("missing immutable request binding", path)
		}
	}
	for _, path := range []string{"", "/{threadId}/history"} {
		route := catalog["GET /api/v1/pages/{slug}/slides/comments"+path]
		if len(route.Parameters) != 2 {
			t.Fatal("pagination contract omitted", path)
		}
	}
	components := slideCommentComponents()
	if len(components) != 7 {
		t.Fatal("missing actual response components", len(components))
	}
	anchor := components["SlideCommentAnchor"].(map[string]any)
	if anchor["properties"].(map[string]any)["anchor_schema_version"] == nil {
		t.Fatal("deck schema used instead of anchor schema")
	}
}
