package main

// This slice describes only registered creation and availability operations.
func artifactProjectCreateSchemaCatalog() map[string]DomainSchema {
	return map[string]DomainSchema{
		"POST /api/v1/pages/artifacts":             {Request: ref("ArtifactProjectCreateRequest"), RequestRequired: true, Response: ref("ArtifactProjectCreateResponse"), SuccessStatuses: []string{"201"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}},
		"GET /api/v1/pages/artifacts/availability": {NoRequest: true, Response: ref("ArtifactProjectAvailability"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"401", "403", "500"}},
	}
}
