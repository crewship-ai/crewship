package main

import (
	"encoding/json"
	"testing"
)

// The cases below are the response-schema findings of the stage API fuzz leg:
// each one is what a live handler answers, and the CLI decodes the same shape.

func specSuccessSchema(t *testing.T, doc map[string]any, path string) map[string]any {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item["get"].(map[string]any)
	responses, _ := op["responses"].(map[string]any)
	ok, _ := responses["200"].(map[string]any)
	content, _ := ok["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, found := media["schema"].(map[string]any)
	if !found {
		t.Fatalf("GET %s declares no JSON 200 schema", path)
	}
	return resolveSpecRef(t, doc, schema)
}

func resolveSpecRef(t *testing.T, doc, schema map[string]any) map[string]any {
	t.Helper()
	name, ok := schema["$ref"].(string)
	if !ok {
		return schema
	}
	const prefix = "#/components/schemas/"
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	target, found := schemas[name[len(prefix):]].(map[string]any)
	if !found {
		t.Fatalf("unresolved %s", name)
	}
	return target
}

func TestFuzzFindings_SuccessShapeMatchesTheHandler(t *testing.T) {
	doc := loadSpec(t)
	for _, tc := range []struct {
		path     string
		wantType string
		property string
	}{
		{"/api/v1/admin/keeper/requests", "array", ""},
		{"/api/v1/agent-load", "array", ""},
		{"/api/v1/crews/{crewId}/peer-conversations", "array", ""},
		{"/api/v1/crews/{crewId}/port-expose", "array", ""},
		{"/api/v1/agents/{agentId}/credential-bindings", "object", "slots"},
		{"/api/v1/agents/{agentId}/notification-channels", "object", "channels"},
		{"/api/v1/agents/{agentId}/persona/history", "object", "entries"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			schema := specSuccessSchema(t, doc, tc.path)
			if schema["type"] != tc.wantType {
				t.Fatalf("type = %v, want %s", schema["type"], tc.wantType)
			}
			if tc.property == "" {
				return
			}
			properties, _ := schema["properties"].(map[string]any)
			if _, ok := properties[tc.property]; !ok {
				t.Fatalf("no %q property in %v", tc.property, keysOf(properties))
			}
		})
	}
}

func TestFuzzFindings_NullableFieldsAreDeclared(t *testing.T) {
	doc := loadSpec(t)
	chat := specSuccessSchema(t, doc, "/api/v1/agents/{agentId}/chats")
	items, _ := chat["items"].(map[string]any)
	properties, _ := items["properties"].(map[string]any)
	for _, field := range []string{"title", "ended_at", "origin"} {
		property, _ := properties[field].(map[string]any)
		if property["nullable"] != true {
			t.Errorf("chat %s is not nullable: %v", field, property)
		}
	}

	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	agent, _ := schemas["Agent"].(map[string]any)
	agentProperties, _ := agent["properties"].(map[string]any)
	crew, _ := agentProperties["crew"].(map[string]any)
	if crew["nullable"] != true {
		t.Errorf("Agent.crew is not nullable: %v", crew)
	}
}

// Middleware answers {"error"} ahead of a handler that replies with RFC 7807,
// and a handler can write Problem Details without calling writeProblem.
func TestFuzzFindings_ErrorEnvelopesAreBothDeclared(t *testing.T) {
	doc := loadSpec(t)
	for _, tc := range []struct{ path, method, status string }{
		{"/api/v1/workspaces/{workspaceId}/skills/{skillId}", "delete", "403"},
		{"/api/v1/workspaces/{workspaceId}/skills/bulk-import", "post", "403"},
		{"/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/files", "get", "403"},
		{"/api/v1/workspaces/{workspaceId}/skills/{skillId}/archive", "post", "403"},
		{"/api/v1/skills/{skillId}", "get", "404"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			content := errorContentOf(t, doc, tc.path, tc.method, tc.status)
			media, _ := content["application/json"].(map[string]any)
			schema, _ := media["schema"].(map[string]any)
			variants, _ := schema["oneOf"].([]any)
			if len(variants) != 2 {
				body, _ := json.Marshal(schema)
				t.Fatalf("%s schema is not error|problem: %s", tc.status, body)
			}
		})
	}
}
