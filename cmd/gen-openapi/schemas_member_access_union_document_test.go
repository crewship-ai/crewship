package main

import (
	"encoding/json"
	"testing"
)

func TestMemberAccessProjectRoutineAliasDocumentContract(t *testing.T) {
	doc := buildDocument(nil)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for name, operations := range map[string][]string{
		"MemberAccessProjectRight": {"list", "read", "write", "delete", "contribute", "manage", "delete_issues"},
		"MemberAccessRoutineRight": {"view", "results", "run", "edit"},
	} {
		schema, ok := schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("generated document lacks the reusable %s component", name)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s permits undeclared fields", name)
		}
		props := schema["properties"].(map[string]any)
		got, err := json.Marshal(props["operation"].(map[string]any)["enum"])
		want, _ := json.Marshal(operations)
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s changed primitive operations: %s, want %s", name, got, want)
		}
	}
	if schemas["MemberAccessRight"] == nil {
		t.Fatal("generated document lacks the complete named member-right union")
	}
	props := memberAccessPolicySchema()["properties"].(map[string]any)
	for _, key := range []string{"rights", "reissue"} {
		items := props[key].(map[string]any)["items"].(map[string]any)
		if items["$ref"] != "#/components/schemas/MemberAccessRight" {
			t.Fatalf("%s does not reuse the complete named member-right union", key)
		}
	}
}
