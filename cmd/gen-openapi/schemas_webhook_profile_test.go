package main

import (
	"reflect"
	"strings"
	"testing"
)

// Assert the final operation after every catalog override, not just the
// activity catalog whose correct fields can be replaced by a later catalog.
func TestWebhookProfileSurvivesFinalDocumentCatalogs(t *testing.T) {
	const path = "/api/v1/workspaces/{workspaceId}/pipeline-webhooks"
	doc := buildDocument([]route{{method: "GET", path: path}, {method: "POST", path: path}})
	ops := doc["paths"].(map[string]any)[path].(map[string]any)
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	resolve := func(schema map[string]any) map[string]any {
		if ref, ok := schema["$ref"].(string); ok {
			return components[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
		}
		return schema
	}
	post := ops["post"].(map[string]any)
	request := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	list := resolve(responseSchemaFromOperation(t, ops["get"].(map[string]any)))
	for name, schema := range map[string]map[string]any{
		"create request":  resolve(request),
		"create response": resolve(responseSchemaFromOperation(t, post)),
		"list response":   resolve(list["items"].(map[string]any)),
	} {
		t.Run(name, func(t *testing.T) {
			profile, ok := schema["properties"].(map[string]any)["ingress_profile"].(map[string]any)
			if !ok {
				t.Fatal("final operation schema omits ingress_profile")
			}
			if profile["type"] != "string" || !reflect.DeepEqual(profile["enum"], []string{"crewship", "github", "unsigned"}) {
				t.Fatalf("ingress_profile must model all supported signature profiles: %#v", profile)
			}
			if name == "create request" && profile["default"] != "crewship" {
				t.Fatalf("create default = %v, want crewship", profile["default"])
			}
		})
	}
}
