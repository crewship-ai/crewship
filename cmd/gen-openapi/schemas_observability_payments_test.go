package main

import "testing"

func TestObservabilityPaymentsCatalogUsesConcreteContracts(t *testing.T) {
	catalog := observabilityPaymentsSchemaCatalog()
	checks := map[string][]string{
		"GET /api/v1/journal":                 {"entries", "next_cursor", "count"},
		"GET /api/v1/paymaster/spend/by-crew": {"rows", "since", "until"},
		"GET /api/v1/metrics/timeseries":      {"metric", "buckets", "series_labels"},
		"GET /api/v1/feature-flags":           {"array"},
		"GET /api/v1/presence/roster":         {"rows", "count"},
		"GET /api/v1/notification-deliveries": {"deliveries"},
	}
	for route, properties := range checks {
		schema, ok := catalog[route]
		if !ok || schema.Response == nil {
			t.Fatalf("%s missing response schema", route)
		}
		if contains(properties, "array") {
			if schema.Response["type"] != "array" || schema.Response["items"] == nil {
				t.Fatalf("%s response is not a concrete array schema", route)
			}
			continue
		}
		props, ok := schema.Response["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s response is not an object schema", route)
		}
		for _, property := range properties {
			if _, ok := props[property]; !ok {
				t.Errorf("%s missing property %q", route, property)
			}
		}
	}
}

func TestObservabilityPaymentsCatalogOverridesGenericEntries(t *testing.T) {
	catalog := routeSchemaCatalog()
	for _, route := range []string{
		"GET /api/v1/journal",
		"GET /api/v1/paymaster/spend/by-crew",
		"GET /api/v1/metrics/timeseries",
		"GET /api/v1/system/runtime",
		"GET /api/v1/notification-channels",
	} {
		schema := catalog[route].Response
		if schema == nil || (schema["type"] == "object" && schema["additionalProperties"] == true) {
			t.Fatalf("%s still has a generic response schema: %#v", route, schema)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
