package main

import "testing"

func TestFinalCoreRequestCatalogCoversAllRemainingGenericBodies(t *testing.T) {
	routes, components := finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog()
	// A count, not a set: the property that matters is the loop below (every
	// entry is a real component ref, and none of them is the bare
	// `{"type":"object"}` fallback this catalog exists to replace). The number
	// is a ratchet on top of it — adding a route here should be a deliberate
	// act, not something that rides along in a diff.
	//
	// Bump it when you add one. 29 as of #1845's POST /refresh-image.
	if len(routes) != 29 {
		t.Fatalf("catalog has %d routes, want 29 — if you added a no-body POST here, bump this; "+
			"if you did not, a route lost its concrete request schema", len(routes))
	}
	for route, contract := range routes {
		ref, ok := contract.Request["$ref"].(string)
		if !ok || ref == "" {
			t.Fatalf("%s request is not a component ref: %#v", route, contract.Request)
		}
		name := ref[len("#/components/schemas/"):]
		schema, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("%s references missing component %q", route, name)
		}
		if schema["type"] == "object" && len(schema) == 1 {
			t.Fatalf("%s retains a generic object request schema", route)
		}
	}
}

func TestFinalCoreRequestCatalogPinsHandlerFields(t *testing.T) {
	_, components := finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog()
	checks := map[string][]string{
		"FinalCoreBootstrapRequest":             {"full_name", "email", "password"},
		"FinalCoreAgentRehireRequest":           {"ttl_minutes", "reason"},
		"FinalCoreWorkspaceCapabilitiesRequest": {"set", "grant", "revoke", "preset"},
		"FinalCoreIssueReviewRequest":           {"action", "comment", "reassign_to"},
		"FinalCoreInboxBulkRequest":             {"ids", "state", "resolved_action"},
		"FinalCoreRefreshToolsRequest":          {"tools"},
	}
	for component, fields := range checks {
		properties := components[component].(map[string]any)["properties"].(map[string]any)
		for _, field := range fields {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s missing handler field %s", component, field)
			}
		}
	}
}

func TestFinalCoreRequestCatalogPreservesExistingResponses(t *testing.T) {
	routes, _ := finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog()
	if got := mergeDomainSchema(DomainSchema{Response: map[string]any{"$ref": "#/components/schemas/ExistingResponse"}}, routes["POST /api/v1/chats/{chatId}/steer"]).Response["$ref"]; got != "#/components/schemas/ExistingResponse" {
		t.Fatalf("response was replaced while merging request schema: %v", got)
	}
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/chats/{chatId}/steer"}})
	response := doc["paths"].(map[string]any)["/api/v1/chats/{chatId}/steer"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)
	got := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
	if got != "#/components/schemas/FinalChatSteer" {
		t.Fatalf("chat steer response schema = %v, want FinalChatSteer", got)
	}
}

func TestFinalCoreRequestCatalogIsWiredIntoDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/bootstrap"}})
	op := doc["paths"].(map[string]any)["/api/v1/bootstrap"].(map[string]any)["post"].(map[string]any)
	body := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["$ref"] != "#/components/schemas/FinalCoreBootstrapRequest" {
		t.Fatalf("bootstrap request schema = %#v", body)
	}
}
