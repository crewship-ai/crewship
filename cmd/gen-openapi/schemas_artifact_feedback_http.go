package main

func artifactProjectFeedbackSchemaCatalog() map[string]DomainSchema {
	routes := map[string]DomainSchema{}
	add := func(method, suffix, response, request, status string) {
		definition := DomainSchema{Response: ref(response), SuccessStatuses: []string{status}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}}
		if request == "" {
			definition.NoRequest = true
		} else {
			definition.Request, definition.RequestRequired = ref(request), true
		}
		routes[method+" /api/v1/pages/{slug}/artifact"+suffix] = definition
	}
	add("GET", "/comments", "ArtifactProjectSnapshot", "", "200")
	add("POST", "/restore", "ArtifactProjectMutationResponse", "ArtifactProjectVersionRequest", "200")
	add("POST", "/share", "ArtifactProjectShareResponse", "ArtifactProjectVersionRequest", "200")
	add("POST", "/comments", "ArtifactProjectComment", "ArtifactProjectCommentRequest", "201")
	add("POST", "/comments/{commentId}/reply", "ArtifactProjectReply", "ArtifactProjectReplyRequest", "200")
	return routes
}
