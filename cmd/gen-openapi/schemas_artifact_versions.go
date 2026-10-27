package main

func artifactProjectVersionSchemaCatalog() map[string]DomainSchema {
	routes := map[string]DomainSchema{}
	for _, path := range []string{"/api/v1/pages/{slug}/artifact", "/api/v1/pages/{slug}/artifact/versions"} {
		routes["GET "+path] = DomainSchema{NoRequest: true, Response: ref("ArtifactProjectSnapshot"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "500", "503"}}
	}
	routes["POST /api/v1/pages/{slug}/artifact/versions"] = DomainSchema{Request: ref("ArtifactProjectPublishRequest"), RequestRequired: true, Response: ref("ArtifactProjectMutationResponse"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}}
	return routes
}
