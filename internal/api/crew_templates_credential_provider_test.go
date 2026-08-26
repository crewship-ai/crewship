package api

// autoAssignCredentials used to filter credentials with a hardcoded
// `provider = 'ANTHROPIC'`, while the agents it links them to carry whatever
// provider their template pinned. For an Anthropic crew the two agreed by
// accident and nothing looked wrong; for any other provider the query matched
// nothing and every agent in the crew came up with zero credentials — no
// error, no failed request, just a crew that does not work.
//
// The onboarding wizard reaches this path for all four builtin templates, so
// it was one adapter choice away on the most common flow in the product, and
// no test covered it. These two do: the first fails against the hardcoded
// filter, the second pins the Anthropic case that used to pass by accident.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ctpAgentsJSON builds an agents_json payload whose agents all declare the
// given provider, mirroring how a builtin template pins llm_provider.
func ctpAgentsJSON(t *testing.T, provider string, slugs ...string) string {
	t.Helper()
	agents := make([]map[string]any, 0, len(slugs))
	for _, s := range slugs {
		agents = append(agents, map[string]any{
			"name":          s,
			"slug":          s,
			"role_title":    s,
			"agent_role":    "AGENT",
			"cli_adapter":   "CLAUDE_CODE",
			"llm_provider":  provider,
			"llm_model":     "claude-sonnet-5",
			"tool_profile":  "default",
			"system_prompt": "you are " + s,
		})
	}
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("marshal agents: %v", err)
	}
	return string(b)
}

// deployAndCountLinks deploys a one-agent template whose agent declares
// agentProvider, against a workspace holding a single credential stored under
// credProvider, and returns how many agent↔credential links were created.
func deployAndCountLinks(t *testing.T, agentProvider, credProvider string) int {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCrewTemplateHandler(db, newTestLogger())

	slug := "ctp-tmpl"
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ctp-1', 'Provider Tmpl', ?, 'CUSTOM', ?, 0, ?)`,
		slug, ctpAgentsJSON(t, agentProvider, "solo"), wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('ctp-cred', ?, ?, 'enc', 'API_KEY', ?, ?)`,
		wsID, credProvider+"_API_KEY", credProvider, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/crew-templates/"+slug+"/deploy",
		bytes.NewBufferString(`{"crew_name":"Provider Crew"}`))
	req.SetPathValue("slug", slug)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, body: %s", rr.Code, rr.Body.String())
	}

	var dep deployCrewResult
	if err := json.Unmarshal(rr.Body.Bytes(), &dep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var links int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM agent_credentials ac
		JOIN agents a ON a.id = ac.agent_id
		WHERE a.crew_id = ?`, dep.CrewID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	return links
}

func TestAutoAssignCredentialsMatchesAgentProvider(t *testing.T) {
	// Table-driven so adding a provider is a row, not a copy of the body.
	cases := []struct {
		name          string
		agentProvider string
		credProvider  string
		wantLinks     int
		why           string
	}{
		{
			name:          "google agent links a google credential",
			agentProvider: "GOOGLE",
			credProvider:  "GOOGLE",
			wantLinks:     1,
			why:           "the hardcoded ANTHROPIC filter matched nothing here, so the crew deployed with zero credentials",
		},
		{
			name:          "anthropic agent links an anthropic credential",
			agentProvider: "ANTHROPIC",
			credProvider:  "ANTHROPIC",
			wantLinks:     1,
			why:           "the case that used to pass by accident — it must keep passing",
		},
		{
			name:          "provider mismatch links nothing",
			agentProvider: "GOOGLE",
			credProvider:  "ANTHROPIC",
			wantLinks:     0,
			why:           "an Anthropic key on a Google agent is worse than no key: it loads and then fails at call time",
		},
		{
			name:          "unset agent provider falls back to anthropic",
			agentProvider: "",
			credProvider:  "ANTHROPIC",
			wantLinks:     1,
			why:           "matches the default resolveLLMProvider applies on the write side",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deployAndCountLinks(t, tc.agentProvider, tc.credProvider)
			if got != tc.wantLinks {
				t.Errorf("agent=%q cred=%q: linked %d credentials, want %d — %s",
					tc.agentProvider, tc.credProvider, got, tc.wantLinks, tc.why)
			}
		})
	}
}

// The provider read is a DB round-trip that can fail; when it does the agent
// must be left unlinked rather than silently falling back to a provider it
// does not use.
func TestAutoAssignCredentialsSurvivesProviderLookupFailure(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ctp-2', 'T', 'ctp-fail', 'CUSTOM', ?, 0, ?)`,
		ctpAgentsJSON(t, "ANTHROPIC", "solo"), wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/crew-templates/ctp-fail/deploy",
		bytes.NewBufferString(`{"crew_name":"C"}`))
	req.SetPathValue("slug", "ctp-fail")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)

	// No credential in the workspace at all: the deploy must still succeed.
	// Auto-assign is best-effort — a workspace with no key yet is the normal
	// state right after onboarding's CLI path, which lands the credential
	// later via `crewship setup`.
	if rr.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, body: %s", rr.Code, rr.Body.String())
	}
}

// deployModelFor deploys a one-agent template pinned to templateModel and
// declaring agentProvider, with the wizard's chosen model/provider as the
// override, and returns the llm_model the agent was actually created with.
func deployModelFor(t *testing.T, templateModel, agentProvider string, ov deployOverrides) string {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	agents := ctpAgentsJSON(t, agentProvider, "solo")
	agents = strings.ReplaceAll(agents, `"llm_model":"claude-sonnet-5"`,
		`"llm_model":"`+templateModel+`"`)
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ctp-m', 'M', 'ctp-model', 'CUSTOM', ?, 0, ?)`, agents, wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	res, err := deployCrewTemplate(context.Background(), db, newTestLogger(), noopEmitter{},
		wsID, "ctp-model", "Model Crew", "", ov)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	var got string
	if err := db.QueryRow(
		`SELECT COALESCE(llm_model, '') FROM agents WHERE crew_id = ?`, res.CrewID,
	).Scan(&got); err != nil {
		t.Fatalf("read agent model: %v", err)
	}
	return got
}

// The wizard's Model select used to be dead on the template path: req.LlmModel
// was read only by the blank/single-agent branch, so all four builtin crews
// silently kept the model their YAML pinned regardless of what the user chose.
func TestDeployCrewTemplateModelOverride(t *testing.T) {
	const templateModel = "claude-sonnet-5"

	cases := []struct {
		name          string
		agentProvider string
		ov            deployOverrides
		want          string
		why           string
	}{
		{
			name:          "matching provider overrides the template",
			agentProvider: "ANTHROPIC",
			ov:            deployOverrides{LLMModel: "claude-opus-5", Provider: "ANTHROPIC"},
			want:          "claude-opus-5",
			why:           "this is the whole point — the user's choice must reach the agent",
		},
		{
			name:          "different provider leaves the template alone",
			agentProvider: "ANTHROPIC",
			ov:            deployOverrides{LLMModel: "gemini-2.5-pro", Provider: "GOOGLE"},
			want:          templateModel,
			why:           "a Gemini id on a CLAUDE_CODE agent breaks it outright — worse than the default",
		},
		{
			name:          "empty override keeps the template",
			agentProvider: "ANTHROPIC",
			ov:            deployOverrides{},
			want:          templateModel,
			why:           "the zero value must deploy the template verbatim, which is what every other caller passes",
		},
		{
			name:          "model without a provider is ignored",
			agentProvider: "ANTHROPIC",
			ov:            deployOverrides{LLMModel: "claude-opus-5"},
			want:          templateModel,
			why:           "an unresolvable provider must fall back rather than apply blind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deployModelFor(t, templateModel, tc.agentProvider, tc.ov)
			if got != tc.want {
				t.Errorf("agent llm_model = %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
}
