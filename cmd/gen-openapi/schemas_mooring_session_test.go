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

// A reauthorization response is neither content nor a Saved receipt. Clients
// must not wait for a fictitious JSON200 or supply a replacement identity.
func TestMooringReauthorizeIsOriginalConnectionAndNoContent(t *testing.T) {
	const path = "/api/v1/pages/{slug}/mooring/session/reauthorize"
	doc := buildDocument([]route{{method: "POST", path: path, auth: true, ws: true}})
	op := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	responses := op["responses"].(map[string]any)
	if responses["200"] != nil {
		t.Fatal("reauthorize falsely advertises JSON200")
	}
	success, ok := responses["204"].(map[string]any)
	if !ok || success["content"] != nil || success["headers"] == nil {
		t.Fatal("reauthorize lost bodyless no-store response", success)
	}
	request := op["requestBody"].(map[string]any)
	if request["required"] != true {
		t.Fatal("missing original connection is not a request")
	}
	schema := request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["$ref"] != "#/components/schemas/MooringReauthorizeRequest" {
		t.Fatal("missing exact request schema", schema)
	}
	input := doc["components"].(map[string]any)["schemas"].(map[string]any)["MooringReauthorizeRequest"].(map[string]any)
	properties := input["properties"].(map[string]any)
	if input["additionalProperties"] != false || len(properties) != 1 || properties["connection_ref"] == nil {
		t.Fatal("reauthorize accepts authority overrides", input)
	}
	// RequireWorkspace accepts either the query or X-Workspace-ID header.
	// The scope is mandatory; neither individual transport source is mandatory.
	query, header, origin := false, false, false
	for _, parameter := range op["parameters"].([]map[string]any) {
		if parameter["name"] == "workspace_id" && parameter["in"] == "query" {
			query = parameter["required"] == false
		}
		if parameter["name"] == "X-Workspace-ID" && parameter["in"] == "header" {
			header = parameter["required"] == false
		}
		if parameter["name"] == "Origin" && parameter["in"] == "header" {
			origin = parameter["required"] == true
		}
	}
	if !query || !header || !origin {
		t.Fatal("scope alternatives or exact Origin contract lost")
	}
	required := input["required"].([]string)
	if len(required) != 1 || required[0] != "connection_ref" {
		t.Fatal("original reference not required", required)
	}
}

func TestMooringCommandSessionKeepsNativeStateOpaqueAndAuthorityOutOfInput(t *testing.T) {
	const path = "/api/v1/pages/{slug}/mooring/command-session"
	doc := buildDocument([]route{{method: "POST", path: path, auth: true, ws: true}})
	post := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	body := post["requestBody"].(map[string]any)
	if body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/MooringCommandSessionRequest" {
		t.Fatal("command session lost named request")
	}
	schema := doc["components"].(map[string]any)["schemas"].(map[string]any)
	input := schema["MooringCommandSessionRequest"].(map[string]any)
	fields := input["properties"].(map[string]any)
	if input["additionalProperties"] != false || len(fields) != 5 {
		t.Fatal("command request is no longer closed", input)
	}
	for _, name := range []string{"role", "actor", "participant", "engine", "checkpoint_base64", "token", "origin"} {
		if fields[name] != nil {
			t.Fatal("authority or replacement state advertised as caller input", name)
		}
	}
	projection := schema["MooringCommandProjection"].(map[string]any)["properties"].(map[string]any)
	if projection["native_json"].(map[string]any)["type"] != "string" {
		t.Fatal("native state must remain opaque")
	}
	response := post["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/MooringCommandSession" {
		t.Fatal("command session lost named response")
	}
	output := schema["MooringCommandSession"].(map[string]any)
	if output["additionalProperties"] != false || len(output["required"].([]string)) != 6 || output["properties"].(map[string]any)["outcome"].(map[string]any)["$ref"] != "#/components/schemas/MooringCommandOutcome" {
		t.Fatal("read or unknown outcome must not imply a saved receipt", output)
	}
	for _, name := range output["required"].([]string) {
		if name == "editor" {
			t.Fatal("editor has omitempty on the actual response DTO")
		}
	}
	outcome := schema["MooringCommandOutcome"].(map[string]any)
	if outcome["type"] != "object" || outcome["nullable"] != true {
		t.Fatal("resolved OAS3 outcome must accept actual null read responses", outcome)
	}
}
