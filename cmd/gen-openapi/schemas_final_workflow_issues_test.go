package main

import "testing"

func TestFinalWorkflowIssueCatalogAuditsRequestedRoutes(t *testing.T) {
	catalog := finalWorkflowIssueSchemaCatalog()
	for _, route := range []string{"GET /api/v1/recurring-issues", "GET /api/v1/triage-rules", "POST /api/v1/triage/process", "GET /api/v1/projects/{projectId}/milestones", "GET /api/v1/crews/{crewId}/issues/{identifier}/relations", "GET /api/v1/crews/{crewId}/escalations", "GET /api/v1/issues", "GET /api/v1/runs", "GET /api/v1/runs/insights", "GET /api/v1/checkpoints/{id}"} {
		if schema, ok := catalog[route]; !ok || schema.Response == nil {
			t.Errorf("missing audited response schema for %s", route)
		}
	}
}

func TestFinalWorkflowIssueCatalogContainsNoGenericTopLevelResponses(t *testing.T) {
	for route, schema := range finalWorkflowIssueSchemaCatalog() {
		if schema.Response != nil && schema.Response["type"] == "object" && len(schema.Response) == 1 {
			t.Errorf("%s has generic object response", route)
		}
	}
}
