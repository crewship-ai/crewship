package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentList_ReportsWebhookConfigurationWithoutDisclosingSecrets(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('webhook-catalog-crew', ?, 'Catalog', 'catalog')`, ws)
	seedAgentRow(t, db, "webhook-configured", ws, "webhook-catalog-crew", "Configured", "configured", "AGENT")
	seedAgentRow(t, db, "webhook-empty", ws, "webhook-catalog-crew", "Empty", "empty", "AGENT")
	seedAgentRow(t, db, "webhook-null", ws, "webhook-catalog-crew", "Null", "null", "AGENT")
	const secret = "test-secret-must-not-leave-agent-catalog"
	execOrFatal(t, db, `UPDATE agents SET webhook_secret = ? WHERE id = 'webhook-configured'`, secret)
	execOrFatal(t, db, `UPDATE agents SET webhook_secret = '' WHERE id = 'webhook-empty'`)
	h := NewAgentHandler(db, newTestLogger())
	for _, query := range []string{"", "&crew_id=webhook-catalog-crew"} {
		req := httptest.NewRequest("GET", "/api/v1/agents?workspace_id="+ws+query, nil)
		req = req.WithContext(withWorkspace(req.Context(), ws, "OWNER"))
		out := httptest.NewRecorder()
		h.List(out, req)
		if out.Code != 200 {
			t.Fatalf("list status %d", out.Code)
		}
		if strings.Contains(out.Body.String(), secret) || strings.Contains(out.Body.String(), `"webhook_secret":`) {
			t.Fatal("catalog discloses secret")
		}
		var rows []agentResponse
		if err := json.Unmarshal(out.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3", len(rows))
		}
		for _, row := range rows {
			if row.WebhookSecretSet == nil || *row.WebhookSecretSet != (row.ID == "webhook-configured") {
				t.Errorf("%s: configuration = %v", row.ID, row.WebhookSecretSet)
			}
		}
	}
}
