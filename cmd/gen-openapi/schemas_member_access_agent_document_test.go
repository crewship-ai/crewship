package main

import "testing"

func TestMemberAccessAgentAliasDocumentContract(t *testing.T) {
	doc := buildDocument(nil)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	agent, ok := schemas["MemberAccessAgentRight"].(map[string]any)
	if !ok {
		t.Fatal("generated document lacks the reusable MemberAccessAgentRight component")
	}
	if agent["additionalProperties"] != false {
		t.Fatal("named agent right permits undeclared fields")
	}
	props := memberAccessPolicySchema()["properties"].(map[string]any)
	for _, key := range []string{"rights", "reissue"} {
		items := props[key].(map[string]any)["items"].(map[string]any)
		if items["$ref"] == "#/components/schemas/MemberAccessRight" {
			items = schemas["MemberAccessRight"].(map[string]any)
		}
		variants := items["oneOf"].([]any)
		if variants[0].(map[string]any)["$ref"] != "#/components/schemas/MemberAccessAgentRight" {
			t.Fatalf("%s does not reuse the named agent-right component", key)
		}
	}
}
