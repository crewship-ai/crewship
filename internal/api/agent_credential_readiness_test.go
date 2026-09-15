package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// acrSeed builds a workspace, a crew and one agent on the given adapter.
func acrSeed(t *testing.T, adapter, llmProvider, llmModel string) (*sql.DB, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	const crewID, agentID = "acr-crew", "acr-agent"
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Quality', 'quality')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, cli_adapter, llm_provider, llm_model)
		VALUES (?, ?, ?, 'Parity Probe', 'parity-probe', ?, ?, ?)`, agentID, crewID, wsID, adapter, llmProvider, llmModel)
	return db, userID, wsID, crewID, agentID
}

// acrCredential inserts an ACTIVE credential of the given type/provider.
func acrCredential(t *testing.T, db *sql.DB, wsID, userID, id, name, credType, provider string) {
	t.Helper()
	seedCredentialEnc(t, db, wsID, userID, id, name, "value-of-"+id)
	execOrFatal(t, db, `UPDATE credentials SET type = ?, provider = ? WHERE id = ?`, credType, provider, id)
}

// acrGet drives the handler as the given role. Provider accounts (a login,
// an LLM API key) are hidden from every management listing below manage
// (credentialVisibilityFilter), so the NAME on a ready verdict follows the
// same rule while the verdict itself does not.
func acrGet(t *testing.T, db *sql.DB, wsID, agentID, role string) (int, agentCredentialReadinessResponse, string) {
	t.Helper()
	h := NewAgentHandler(db, newTestLogger())
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/credential-readiness", nil), wsID)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, role))
	req.SetPathValue("agentId", agentID)
	w := httptest.NewRecorder()
	h.CredentialReadiness(w, req)
	var out agentCredentialReadinessResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return w.Code, out, w.Body.String()
}

// TestAgentCredentialReadiness is the issue's own matrix (#2183). The first
// case is #2169 as measured on dev3: a CLAUDE_CODE agent whose only reachable
// credential is the crew's GH_TOKEN binding. `GET …/credentials` returns one
// row for it, so the old zero-length guard stayed quiet; the run then failed
// at the first model call.
func TestAgentCredentialReadiness(t *testing.T) {
	type seedFn func(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string)
	ghBinding := func(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string) {
		acrCredential(t, db, wsID, userID, "cred-gh", "github-globex", "CLI_TOKEN", "GITHUB")
		execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, crew_id, created_at)
			VALUES ('cb-gh', ?, 'cred-gh', 'GH_TOKEN', 'CREW', ?, datetime('now'))`, wsID, crewID)
	}
	claudeGrant := func(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string) {
		acrCredential(t, db, wsID, userID, "cred-claude", "CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", "ANTHROPIC")
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES ('ac-claude', ?, 'cred-claude', 'CLAUDE_CODE_OAUTH_TOKEN', 0, datetime('now'))`, agentID)
	}
	openaiCrewLink := func(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string) {
		acrCredential(t, db, wsID, userID, "cred-openai", "OPENAI_API_KEY", "API_KEY", "OPENAI")
		execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred-openai', ?)`, crewID)
	}

	cases := []struct {
		name                            string
		adapter, llmProvider, llmModel  string
		seed                            []seedFn
		wantState, wantName, wantSource string
		wantDelivery, wantProvider      string
	}{
		{
			name:    "claude code with only a crew GH_TOKEN binding is missing",
			adapter: "CLAUDE_CODE", llmProvider: "ANTHROPIC", llmModel: "claude-sonnet-4-5",
			seed:      []seedFn{ghBinding},
			wantState: "missing", wantProvider: "ANTHROPIC",
		},
		{
			name:    "claude code with an agent-granted login is ready, with its source",
			adapter: "CLAUDE_CODE", llmProvider: "ANTHROPIC", llmModel: "claude-sonnet-4-5",
			seed:      []seedFn{ghBinding, claudeGrant},
			wantState: "ready", wantName: "CLAUDE_CODE_OAUTH_TOKEN", wantSource: "agent_grant",
			wantDelivery: "login_env", wantProvider: "ANTHROPIC",
		},
		{
			name:    "opencode with an OpenAI key is ready",
			adapter: "OPENCODE", llmProvider: "OPENAI", llmModel: "gpt-5.5",
			seed:      []seedFn{ghBinding, openaiCrewLink},
			wantState: "ready", wantName: "OPENAI_API_KEY", wantSource: "crew_link",
			wantDelivery: "env", wantProvider: "OPENAI",
		},
		{
			name:    "an adapter the runtime does not know is unknown",
			adapter: "SOME_FUTURE_CLI", llmProvider: "ANTHROPIC",
			seed:      []seedFn{claudeGrant},
			wantState: "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, userID, wsID, crewID, agentID := acrSeed(t, tc.adapter, tc.llmProvider, tc.llmModel)
			for _, s := range tc.seed {
				s(t, db, userID, wsID, crewID, agentID)
			}
			code, out, body := acrGet(t, db, wsID, agentID, "OWNER")
			if code != http.StatusOK {
				t.Fatalf("status = %d body=%s", code, body)
			}
			if out.AgentID != agentID || out.AgentSlug != "parity-probe" || out.Adapter != tc.adapter {
				t.Errorf("identity wrong: %+v", out)
			}
			mc := out.ModelCredential
			if mc.State != tc.wantState {
				t.Fatalf("state = %q, want %q (body %s)", mc.State, tc.wantState, body)
			}
			if mc.CredentialName != tc.wantName || mc.Source != tc.wantSource || mc.Delivery != tc.wantDelivery || mc.Provider != tc.wantProvider {
				t.Errorf("model_credential = %+v, want name=%q source=%q delivery=%q provider=%q",
					mc, tc.wantName, tc.wantSource, tc.wantDelivery, tc.wantProvider)
			}
			if out.Notes == nil || strings.Contains(body, `"notes":null`) {
				t.Errorf("notes must serialise as [] not null: %s", body)
			}
			if tc.wantState != "ready" && len(out.Notes) == 0 {
				t.Errorf("a %s verdict must explain itself: %s", tc.wantState, body)
			}
			// A report, not a reveal: no credential value may appear.
			if strings.Contains(body, "value-of-") {
				t.Errorf("response carries a credential value: %s", body)
			}

			// Below manage, the provider account's name is redacted exactly
			// as the credentials listing redacts the row; the verdict stays.
			_, asManager, _ := acrGet(t, db, wsID, agentID, "MANAGER")
			if asManager.ModelCredential.State != tc.wantState {
				t.Errorf("state as MANAGER = %q, want %q", asManager.ModelCredential.State, tc.wantState)
			}
			if tc.wantState == "ready" && asManager.ModelCredential.CredentialName != "" {
				t.Errorf("a MANAGER must not see the provider account's name, got %q", asManager.ModelCredential.CredentialName)
			}
		})
	}
}

func TestAgentCredentialReadiness_Errors(t *testing.T) {
	db, _, wsID, _, agentID := acrSeed(t, "CLAUDE_CODE", "ANTHROPIC", "")

	t.Run("missing agentId", func(t *testing.T) {
		h := NewAgentHandler(db, newTestLogger())
		req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/agents//credential-readiness", nil), wsID)
		w := httptest.NewRecorder()
		h.CredentialReadiness(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	// An agent in another workspace must 404, not have its posture described.
	t.Run("agent not in workspace", func(t *testing.T) {
		code, _, _ := acrGet(t, db, "ws-elsewhere", agentID, "OWNER")
		if code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", code)
		}
	})
}
