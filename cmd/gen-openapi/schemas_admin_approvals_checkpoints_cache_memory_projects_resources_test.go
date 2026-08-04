package main

import "testing"

func TestSchemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResourcesAuditsRoutes(t *testing.T) {
	catalog := schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources()
	want := []string{
		"GET /api/v1/admin/stats",
		"GET /api/v1/approvals",
		"POST /api/v1/approvals/{id}/decide",
		"GET /api/v1/missions/{missionId}/checkpoints",
		"GET /api/v1/cache/images",
		"GET /api/v1/admin/memory/stats",
		"GET /api/v1/recipes/{slug}/preview",
		"POST /api/v1/projects",
		"GET /api/v1/crews/{crewId}/capabilities",
	}
	for _, route := range want {
		if _, ok := catalog[route]; !ok {
			t.Errorf("catalog missing audited route %q", route)
		}
	}

	approval := catalog["GET /api/v1/approvals"].Response["properties"].(map[string]any)
	for _, field := range []string{"rows", "status", "count"} {
		if _, ok := approval[field]; !ok {
			t.Errorf("approval list response missing %q", field)
		}
	}
	checkpoint := catalog["GET /api/v1/checkpoints/{id}"].Response["properties"].(map[string]any)
	for _, field := range []string{"journal_cursor", "state", "created_at"} {
		if _, ok := checkpoint[field]; !ok {
			t.Errorf("checkpoint response missing %q", field)
		}
	}
}

func TestSchemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResourcesWiresConcreteBodies(t *testing.T) {
	routes := []route{
		{method: "POST", path: "/api/v1/approvals/{id}/decide"},
		{method: "GET", path: "/api/v1/cache/images"},
		{method: "POST", path: "/api/v1/projects"},
		{method: "GET", path: "/api/v1/crews/{crewId}/capabilities"},
	}
	doc := buildDocument(routes)
	paths := doc["paths"].(map[string]any)
	decide := paths["/api/v1/approvals/{id}/decide"].(map[string]any)["post"].(map[string]any)
	request := decide["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if request["type"] != "object" {
		t.Fatalf("decide request schema = %#v, want concrete object", request)
	}
	cache := paths["/api/v1/cache/images"].(map[string]any)["get"].(map[string]any)
	cacheSchema := responseSchemaFromOperation(t, cache)
	if cacheSchema["type"] != "object" {
		t.Fatalf("cache response schema = %#v, want concrete object", cacheSchema)
	}
	project := paths["/api/v1/projects"].(map[string]any)["post"].(map[string]any)
	projectResponse := responseSchemaFromOperation(t, project)
	if projectResponse["type"] != "object" {
		t.Fatalf("project response schema = %#v, want concrete object", projectResponse)
	}
}

func TestSchemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResourcesReturnsFreshMaps(t *testing.T) {
	one := schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources()
	one["GET /api/v1/admin/stats"] = DomainSchema{}
	if schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources()["GET /api/v1/admin/stats"].Response == nil {
		t.Fatal("catalog returned shared mutable state")
	}
}
