package main

import "testing"

func TestProviderPoolSchemas(t *testing.T) {
	routes, components := providerLoginSchemaCatalog()
	for _, route := range []string{"POST /api/v1/provider-logins/pools", "GET /api/v1/provider-logins/pools", "GET /api/v1/provider-logins/pools/{poolId}"} {
		t.Run(route, func(t *testing.T) {
			if routes[route].Response["$ref"] == nil {
				t.Errorf("missing named response: %s", route)
			}
		})
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

func TestProviderPoolRequestBodyRequired(t *testing.T) {
	if _, ok := loadSpecOperations(t)["/api/v1/provider-logins/pools"]["post"].Responses["400"]; !ok {
		t.Fatal("generated pool create operation omits validation response")
	}
	for _, tc := range []struct {
		path     string
		required bool
	}{
		{"/api/v1/provider-logins/pools", true},
		{"/api/v1/provider-logins/device", false},
		{"/api/v1/credentials", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := requestBodyForRoute(route{method: "POST", path: tc.path})
			if got, _ := body["required"].(bool); got != tc.required {
				t.Fatalf("body required=%v want=%v", got, tc.required)
			}
		})
	}
}

func TestProviderPoolLifecycleContract(t *testing.T) {
	ops := loadSpecOperations(t)
	for _, method := range []string{"put", "delete"} {
		t.Run(method, func(t *testing.T) {
			op := ops["/api/v1/provider-logins/pools/{poolId}"][method]
			found := false
			for _, p := range op.Parameters {
				if p.Name == "If-Match" && p.In == "header" && p.Required {
					found = true
				}
			}
			if !found {
				t.Fatal("missing required precondition header")
			}
			for _, code := range []string{"204", "400", "404", "412", "428"} {
				if _, ok := op.Responses[code]; !ok {
					t.Errorf("missing %s", code)
				}
			}
			if _, ok := op.Responses["200"]; ok {
				t.Fatal("documented success body for no-content operation")
			}
		})
	}
	if body := requestBodyForRoute(route{method: "PUT", path: "/api/v1/provider-logins/pools/{poolId}"}); body["required"] != true {
		t.Fatal("update body optional")
	}
}
