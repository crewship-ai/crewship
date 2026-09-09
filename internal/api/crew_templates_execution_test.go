package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTemplateDeployExecutionChoice(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		status       int
	}{
		{"OpenAI selects Codex", `,"llm_provider":"OPENAI","cli_adapter":"CODEX_CLI","llm_model":"gpt-5.5"`, http.StatusCreated},
		{"provider without runner rejected", `,"llm_provider":"OPENAI"`, http.StatusBadRequest},
		{"mismatched runner rejected", `,"llm_provider":"OPENAI","cli_adapter":"CLAUDE_CODE","llm_model":"gpt-5.5"`, http.StatusBadRequest},
		{"missing model rejected", `,"llm_provider":"OPENAI","cli_adapter":"CODEX_CLI"`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			agents := ctpAgentsJSON(t, "ANTHROPIC", "solo")
			if _, err := db.Exec(`INSERT INTO crew_templates (id,name,slug,category,agents_json,is_builtin,workspace_id) VALUES ('execution','Execution','execution','CUSTOM',?,0,?)`, agents, ws); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/crew-templates/execution/deploy", strings.NewReader(`{"crew_name":"Selected","crew_slug":"selected"`+tc.fields+`}`))
			req.SetPathValue("slug", "execution")
			req = withWorkspaceUser(req, user, ws, "OWNER")
			rec := httptest.NewRecorder()
			h := NewCrewTemplateHandler(db, newTestLogger())
			fake := &rebuildFake{}
			h.SetProvisioner(fake)
			h.Deploy(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusCreated {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM crews WHERE slug = 'selected'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 || len(fake.calls) != 0 {
					t.Fatal("invalid selection created or provisioned a crew")
				}
				return
			}
			var result deployCrewResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var provider, adapter, model string
			if err := db.QueryRow(`SELECT llm_provider, cli_adapter, llm_model FROM agents WHERE crew_id = ?`, result.CrewID).Scan(&provider, &adapter, &model); err != nil {
				t.Fatal(err)
			}
			if provider != "OPENAI" || adapter != "CODEX_CLI" || model != "gpt-5.5" {
				t.Fatalf("execution = %s / %s / %s", provider, adapter, model)
			}
			if len(fake.calls) != 1 || fake.calls[0] != result.CrewID {
				t.Fatalf("preparation calls = %v", fake.calls)
			}
		})
	}
}
