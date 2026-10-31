package main

// This slice describes only registered creation and availability operations.
func artifactProjectCreateSchemaCatalog() map[string]DomainSchema {
	return map[string]DomainSchema{
		"POST /api/v1/pages/artifacts":             {Request: ref("ArtifactProjectCreateRequest"), RequestRequired: true, Response: ref("ArtifactProjectCreateResponse"), SuccessStatuses: []string{"201"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}},
		"GET /api/v1/pages/artifacts/storage":      {NoRequest: true, Response: ref("ArtifactStorageUsage"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"401", "403", "500"}},
		"GET /api/v1/pages/artifacts/availability": {NoRequest: true, Parameters: []map[string]any{{"name": "agent_id", "in": "query", "required": false, "schema": map[string]any{"type": "string"}, "description": "Agent whose crew policy to check. When set the response adds can_create and, if refused, code artifact_crew_not_allowed."}}, Response: ref("ArtifactProjectAvailability"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"401", "403", "500"}},
	}
}
