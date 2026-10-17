package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func expandedMemberAccessSchema(node any) any {
	switch value := node.(type) {
	case map[string]any:
		if target, ok := value["$ref"].(string); ok {
			return expandedMemberAccessSchema(memberAccessSchemaComponents()[strings.TrimPrefix(target, "#/components/schemas/")])
		}
		result := map[string]any{}
		for key, item := range value {
			result[key] = expandedMemberAccessSchema(item)
		}
		return result
	case []any:
		result := []any{}
		for _, item := range value {
			result = append(result, expandedMemberAccessSchema(item))
		}
		return result
	default:
		return node
	}
}
func memberAccessTestRightVariants() []any {
	p := memberAccessPolicySchema()["properties"].(map[string]any)["rights"].(map[string]any)
	return expandedMemberAccessSchema(p["items"]).(map[string]any)["oneOf"].([]any)
}

func TestMemberAccessNamedSchemasPreserveContract(t *testing.T) {
	// Independent copy of the prior inline contract. Expanded refs must preserve
	// every field, operation and constraint, including orthogonal destructive rights.
	prior := func(kind string, operations []string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "id", "operation"}, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{kind}}, "id": map[string]any{"type": "string", "minLength": 1}, "operation": map[string]any{"type": "string", "enum": operations}}}
	}
	expected := map[string]any{"oneOf": []any{prior("agent", []string{"discover", "chat", "run", "delegate"}), prior("project", []string{"list", "read", "write", "delete", "contribute", "manage", "delete_issues"}), prior("routine", []string{"view", "results", "run", "edit"})}}
	props := memberAccessPolicySchema()["properties"].(map[string]any)
	for _, key := range []string{"rights", "reissue"} {
		shape := props[key].(map[string]any)
		if shape["type"] != "array" || shape["maxItems"] != 256 || shape["uniqueItems"] != true {
			t.Fatalf("%s changed replacement constraints", key)
		}
		got, _ := json.Marshal(expandedMemberAccessSchema(shape["items"]))
		want, _ := json.Marshal(expected)
		if string(got) != string(want) {
			t.Fatalf("%s wire contract changed:\n%s\n%s", key, got, want)
		}
	}
	if _, ok := memberEffectiveAccessSchema()["properties"].(map[string]any)["rights"]; ok {
		t.Fatal("effective explanation unexpectedly exposes the administrative policy document")
	}
}
