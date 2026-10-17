package main

import (
	"reflect"
	"testing"
)

func TestMemberAccessNamedGrantPreservesContract(t *testing.T) {
	expected := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "id", "operation", "issuer_id", "created_at", "live"}, "properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "operation": map[string]any{"type": "string"}, "issuer_id": map[string]any{"type": "string"}, "issuer_name": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string"}, "live": map[string]any{"type": "boolean"}}}
	if !reflect.DeepEqual(memberAccessSchemaComponents()["MemberAccessGrant"], expected) {
		t.Fatal("named grant changes prior wire semantics")
	}
	p := memberAccessPolicySchema()["properties"].(map[string]any)
	grant := p["grant_details"].(map[string]any)
	if grant["type"] != "array" || grant["readOnly"] != true || grant["items"].(map[string]any)["$ref"] != "#/components/schemas/MemberAccessGrant" {
		t.Fatal("administrative provenance contract disconnected")
	}
	if _, ok := memberEffectiveAccessSchema()["properties"].(map[string]any)["grant_details"]; ok {
		t.Fatal("personal response exposes admin grant provenance")
	}
}
