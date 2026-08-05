package main

import "testing"

func TestRemainingAdminSystemCatalogCoversAuditedGenericRoutes(t *testing.T) {
	catalog := remainingAdminSystemSchemaCatalogV2()
	routes := []string{
		"GET /api/v1/admin/health", "GET /api/v1/admin/keeper/config", "GET /api/v1/admin/keeper/aux",
		"GET /api/v1/admin/keeper/judge/models", "GET /api/v1/admin/journal/verify", "POST /api/v1/admin/reap-orphan-containers",
		"GET /api/v1/system/setup-status", "GET /api/v1/system/version", "GET /api/v1/system/license",
		"GET /api/v1/system/keeper", "GET /api/v1/system/aux-status", "GET /api/v1/policies", "GET /api/v1/runtimes/catalog",
	}
	for _, route := range routes {
		schema, ok := catalog[route]
		if !ok || schema.Response == nil {
			t.Errorf("catalog missing concrete response for %q", route)
		}
	}
}

func TestRemainingAdminSystemCatalogDoesNotUseGenericTopLevelResponses(t *testing.T) {
	for route, schema := range remainingAdminSystemSchemaCatalogV2() {
		if schema.Response == nil {
			continue
		}
		if _, generic := schema.Response["additionalProperties"]; generic {
			t.Errorf("%s retains generic top-level response schema", route)
		}
	}
}

func TestRemainingAdminSystemCatalogWiredIntoRouteCatalog(t *testing.T) {
	catalog := routeSchemaCatalog()
	checks := map[string][]string{
		"GET /api/v1/admin/health":      {"uptime_seconds", "disk"},
		"GET /api/v1/system/version":    {"current", "schema_version"},
		"GET /api/v1/system/license":    {"edition", "features"},
		"GET /api/v1/policies":          {"crew_id", "autonomy_level", "behavior_mode"},
		"GET /api/v1/system/aux-status": {"subsystems"},
	}
	for route, fields := range checks {
		response := catalog[route].Response
		if response["type"] == "array" {
			response, _ = response["items"].(map[string]any)
		}
		properties, ok := response["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s response is not an object with properties", route)
		}
		for _, field := range fields {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s response missing field %q", route, field)
			}
		}
	}
}

func TestRemainingAdminSystemCatalogReturnsFreshMaps(t *testing.T) {
	first := remainingAdminSystemSchemaCatalogV2()
	first["GET /api/v1/admin/health"] = DomainSchema{}
	if remainingAdminSystemSchemaCatalogV2()["GET /api/v1/admin/health"].Response == nil {
		t.Fatal("catalog returned shared mutable state")
	}
}
