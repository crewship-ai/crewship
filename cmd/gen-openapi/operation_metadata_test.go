package main

import (
	"strings"
	"testing"
)

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

func TestOperationMetadataPublicDescriptions(t *testing.T) {
	for _, tc := range []struct{ method, path, summary string }{
		{"GET", "/api/health", "Get server health"},
		{"GET", "/api/v1/users/{id}/avatar", "Get user avatar"},
		{"GET", "/api/v1/widgets/{id}", "Get widget"},
		{"GET", "/api/v1/widgets/{id}/status", "Get widget status"},
		{"GET", "/api/v1/widgets/{id}/events", "List widget events"},
		{"POST", "/api/v1/widgets/{id}/comments", "Create widget comments"},
		{"POST", "/api/v1/agents/{agentId}/chats/{chatId}/shares", "Create chat share"},
		{"POST", "/api/v1/widgets/{id}/cancel", "Cancel widget"},
		{"POST", "/api/v1/widgets/{id}/preview", "Preview widget"},
		{"GET", "/api/v1/widgets/{id}/metrics", "Get widget metrics"},
		{"POST", "/api/v1/crews/{crewId}/restart-agents", "Restart agents for crew"},
		{"POST", "/api/v1/widgets/{id}/preview-contents", "Preview contents of widget"},
		{"POST", "/api/v1/widgets/test_run", "Test widgets"},
		{"POST", "/api/v1/widgets/{id}/future-action", "Submit widget future action"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			summary, description, _ := operationMetadata(route{method: tc.method, path: tc.path})
			if summary != tc.summary {
				t.Errorf("summary = %q, want %q", summary, tc.summary)
			}
			if strings.Contains(description, "Inspect the parameters") || strings.Contains(summary, "  ") {
				t.Errorf("unhelpful prose: %q %q", summary, description)
			}
		})
	}
}
