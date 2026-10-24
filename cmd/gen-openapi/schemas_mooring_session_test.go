package main

import "testing"

// A WebSocket upgrade must never be described as a JSON 200 or JSON error.
// Clients otherwise wait for a body that cannot exist after the switch.
func TestMooringSessionAndUpgradeWireContracts(t *testing.T) {
	const base = "/api/v1/pages/{slug}/mooring"
	doc := buildDocument([]route{{method: "POST", path: base + "/session", auth: true, ws: true}, {method: "GET", path: base + "/ws", auth: true, ws: true}})
	paths := doc["paths"].(map[string]any)
	post := paths[base+"/session"].(map[string]any)["post"].(map[string]any)
	request := post["requestBody"].(map[string]any)
	if request["required"] != true || request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/MooringSessionRequest" {
		t.Fatalf("session requires a named JSON body: %#v", request)
	}
	response := post["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/MooringSession" {
		t.Fatalf("session response lost named contract: %#v", response)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	input := schemas["MooringSessionRequest"].(map[string]any)
	properties := input["properties"].(map[string]any)
	if input["additionalProperties"] != false || input["required"] != nil || len(properties) != 2 || properties["write"] == nil || properties["checkpoint_base64"] == nil {
		t.Fatalf("session request must allow {} and only write/checkpoint: %#v", input)
	}
	for _, field := range []string{"write", "checkpoint_base64"} {
		if properties[field].(map[string]any)["nullable"] != true {
			t.Fatalf("%s lost the handler's explicit-null-as-omitted contract", field)
		}
	}
	output := schemas["MooringSession"].(map[string]any)
	wantRequired := map[string]bool{"session_id": true, "checkpoint_revision": true, "sdk_prefix": true, "participant_ref": true, "ticket": true, "connection_ref": true, "origin": true, "versions": true, "websocket_path": true}
	if output["additionalProperties"] != false || len(output["required"].([]string)) != len(wantRequired) {
		t.Fatalf("session response lost required fields/closed shape: %#v", output)
	}
	for _, field := range output["required"].([]string) {
		if !wantRequired[field] {
			t.Fatalf("unexpected/duplicate required field %q", field)
		}
		delete(wantRequired, field)
	}
	upgrade := paths[base+"/ws"].(map[string]any)["get"].(map[string]any)
	responses := upgrade["responses"].(map[string]any)
	if _, exists := responses["200"]; exists {
		t.Fatal("WebSocket falsely advertises JSON 200 success")
	}
	switching, ok := responses["101"].(map[string]any)
	if !ok || switching["description"] != "Switching Protocols" || switching["content"] != nil || len(switching["headers"].(map[string]any)) != 2 {
		t.Fatalf("upgrade must be a bodyless 101 with upgrade headers: %#v", switching)
	}
	if upgrade["requestBody"] != nil || len(upgrade["security"].([]map[string][]string)) != 3 {
		t.Fatal("upgrade lost authenticated bodyless request")
	}
	found := false
	for _, p := range upgrade["parameters"].([]map[string]any) {
		if p["name"] == "connection_ref" && p["in"] == "query" && p["required"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("upgrade lost required original connection_ref")
	}
}
