package main

import "testing"

func TestChatShareCatalogWiresTypedFourRoutes(t *testing.T) {
	const management = "/api/v1/agents/{agentId}/chats/{chatId}/shares"
	const reader = "/api/v1/shared-chats/{shareId}/messages"
	doc := buildDocument([]route{
		{method: "POST", path: management, auth: true},
		{method: "GET", path: management, auth: true},
		{method: "DELETE", path: management + "/{shareId}", auth: true},
		{method: "GET", path: reader},
	})
	paths := doc["paths"].(map[string]any)
	checks := []struct {
		path, method, status, schema string
		params                       []string
	}{
		{management, "post", "201", "ChatShareCreateResponse", []string{"agentId", "chatId"}},
		{management, "get", "200", "ChatShareListResponse", []string{"agentId", "chatId"}},
		{management + "/{shareId}", "delete", "204", "", []string{"agentId", "chatId", "shareId"}},
		{reader, "get", "200", "ChatShareMessagesResponse", []string{"shareId"}},
	}
	for _, check := range checks {
		op := paths[check.path].(map[string]any)[check.method].(map[string]any)
		params := op["parameters"].([]map[string]any)
		for _, name := range check.params {
			found := false
			for _, param := range params {
				if param["name"] == name && param["in"] == "path" && param["required"] == true && param["schema"].(map[string]any)["minLength"] == 1 {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s missing nonempty path parameter %s: %#v", check.method, check.path, name, params)
			}
		}
		response := op["responses"].(map[string]any)[check.status].(map[string]any)
		if response["headers"].(map[string]any)["Cache-Control"] == nil {
			t.Errorf("%s %s missing no-store header", check.method, check.path)
		}
		if check.schema != "" {
			content := response["content"].(map[string]any)["application/json"].(map[string]any)
			if got := content["schema"].(map[string]any)["$ref"]; got != "#/components/schemas/"+check.schema {
				t.Errorf("%s %s response schema = %v", check.method, check.path, got)
			}
		} else if response["content"] != nil {
			t.Errorf("DELETE success unexpectedly has body: %#v", response)
		}
	}
	post := paths[management].(map[string]any)["post"].(map[string]any)
	body := post["requestBody"].(map[string]any)
	if body["required"] == true { // handler accepts EOF and uses its default TTL.
		t.Fatal("create request body should be optional")
	}
	if got := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]; got != "#/components/schemas/ChatShareCreateRequest" {
		t.Fatalf("create request schema = %v", got)
	}
	read := paths[reader].(map[string]any)["get"].(map[string]any)
	if read["responses"].(map[string]any)["413"] == nil {
		t.Fatal("bounded transcript reader must document 413")
	}
}

func TestChatShareComponentShapesExcludePrivilegedTranscriptFields(t *testing.T) {
	_, schemas := chatShareSchemaCatalog()
	createRequest := schemas["ChatShareCreateRequest"].(map[string]any)
	if _, required := createRequest["required"]; required {
		t.Fatal("create request accepts an empty body and must have no required fields")
	}
	ttl := createRequest["properties"].(map[string]any)["ttl_seconds"].(map[string]any)
	if ttl["default"] != 86400 || ttl["maximum"] != 604800 || ttl["minimum"] != 0 {
		t.Fatalf("TTL contract = %#v", ttl)
	}
	grant := schemas["ChatShareGrant"].(map[string]any)["properties"].(map[string]any)
	if grant["token_hash"] != nil || grant["token"] != nil {
		t.Fatal("listed grant must never expose token material")
	}
	message := schemas["ChatShareTextMessage"].(map[string]any)
	if message["additionalProperties"] != false {
		t.Fatal("shared message schema must forbid undeclared tool/metadata fields")
	}
	props := message["properties"].(map[string]any)
	if len(props) != 4 || props["id"] == nil || props["role"] == nil || props["content"] == nil || props["created_at"] == nil {
		t.Fatalf("shared text fields = %#v", props)
	}
}
