package main

import "testing"

func TestProviderPoolSchemas(t *testing.T) {
	routes, components := providerLoginSchemaCatalog()
	for _, route := range []string{"POST /api/v1/provider-logins/pools", "GET /api/v1/provider-logins/pools", "GET /api/v1/provider-logins/pools/{poolId}"} {
		if routes[route].Response["$ref"] == nil {
			t.Errorf("missing named response: %s", route)
		}
	}
	assertRequired(t, components["ProviderPoolCreateRequest"], "name", "provider", "mode", "members")
	request := components["ProviderPoolCreateRequest"].(map[string]any)
	if request["additionalProperties"] != false {
		t.Fatal("unknown request fields allowed")
	}
	memberList := request["properties"].(map[string]any)["members"].(map[string]any)
	if memberList["minItems"] != 1 || memberList["maxItems"] != 100 {
		t.Fatal("incorrect pool size bounds")
	}
	props := components["ProviderPool"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"value", "encrypted_value", "generation", "refresh_token"} {
		if _, ok := props[field]; ok {
			t.Fatalf("secret/provenance field: %s", field)
		}
	}
}
