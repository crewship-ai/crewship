package main

import "testing"

func TestOperationMetadataSafetyAndIntent(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		read         bool
	}{
		{"GET", "/api/v1/agents", true},
		{"POST", "/api/v1/conversations/search", true},
		{"POST", "/api/v1/automations/preview", true},
		{"POST", "/api/v1/memory/search/hybrid", true},
		{"POST", "/api/v1/admin/instance/backups/plans/preview-contents", true},
		{"POST", "/api/v1/oauth/discover", true},
		{"POST", "/api/v1/admin/instance/backups/restore/checks", true},
		{"POST", "/api/v1/admin/instance/backups/bundles/check", false},
		{"POST", "/api/v1/future/search", false},
		{"POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run", false},
		{"DELETE", "/api/v1/agents/{id}", false},
	} {
		summary, description, read := operationMetadata(route{method: tc.method, path: tc.path})
		if summary == "" || description == "" || read != tc.read {
			t.Errorf("%+v: %q %q %v", tc, summary, description, read)
		}
	}
}
