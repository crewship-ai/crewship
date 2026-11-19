package main

import "testing"

func TestCrewMiseLockResolveHasTypedInputAndProposal(t *testing.T) {
	rt := routeFromKey("POST /api/v1/crews/{crewId}/provision/lock-resolve")
	doc := buildDocument([]route{rt})
	op := doc["paths"].(map[string]any)[rt.path].(map[string]any)["post"].(map[string]any)
	body := op["requestBody"].(map[string]any)
	request := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["required"] != true || request["$ref"] != "#/components/schemas/CrewMiseLockResolveRequest" || response["$ref"] != "#/components/schemas/CrewMiseLockProposal" {
		t.Fatalf("generic resolver contract: request=%v response=%v", request, response)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	config := schemas["CrewMiseLockResolveRequest"].(map[string]any)["properties"].(map[string]any)["config"].(map[string]any)
	props := config["properties"].(map[string]any)
	if config["additionalProperties"] != false || props["tools"] == nil || props["lock"] == nil || len(props) != 2 {
		t.Fatal("resolver input permits env/tasks/image")
	}
	proposal := schemas["CrewMiseLockProposal"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"tools", "selectors_sha256", "previous_lock_sha256", "lock_sha256", "lock_changed", "lock", "resolver_image_id", "platform", "mise_version"} {
		if proposal[name] == nil {
			t.Errorf("missing proposal field %s", name)
		}
	}
}
