package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// credGateDef builds a routine definition declaring the given
// credentials_required types plus one benign agent_run step.
func credGateDef(types ...string) string {
	reqs := make([]map[string]string, 0, len(types))
	for _, t := range types {
		reqs = append(reqs, map[string]string{"type": t})
	}
	js, _ := json.Marshal(reqs)
	return `{"dsl_version":"1.0","name":"cred-routine","credentials_required":` + string(js) +
		`,"steps":[{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
}

// seedVaultCredential inserts an ACTIVE workspace-shared credential of the
// given type so the run-gate probe can resolve it.
func seedVaultCredential(t *testing.T, db *sql.DB, wsID, userID, id, credType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, status, created_by, created_at)
		VALUES (?, ?, ?, 'enc-x', ?, 'NONE', 'ACTIVE', ?, datetime('now'))`,
		id, wsID, id, credType, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

// seedVaultCredentialFull inserts an ACTIVE credential with an explicit
// provider and crew pin (empty crewID → workspace-shared), so a test can
// reproduce an Anthropic key that only the provider-aware LLM path resolves.
func seedVaultCredentialFull(t *testing.T, db *sql.DB, wsID, userID, id, credType, provider, crewID string) {
	t.Helper()
	var crew any
	if crewID != "" {
		crew = crewID
	}
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, status, crew_id, created_by, created_at)
		VALUES (?, ?, ?, 'enc-x', ?, ?, 'ACTIVE', ?, ?, datetime('now'))`,
		id, wsID, id, credType, provider, crew, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

// TestCredentialGate_AnthropicTokenSatisfiesApiKeyRequirement pins the #1418
// gate blind spot: a routine declaring credentials_required: [api_key] whose
// agent steps consume the Anthropic key must run when the vault holds an
// AI_CLI_TOKEN (OAuth) — the type the seed creates for an sk-ant-oat key. The
// LLM runner resolves either API_KEY or AI_CLI_TOKEN, so the gate must too.
// Before the parity fix this 422'd; after it, the run proceeds.
func TestCredentialGate_AnthropicTokenSatisfiesApiKeyRequirement(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncKeyCredGate)
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_oat", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_oat", wsID, crewID, "Morgan", "morgan", "LEAD")
	// Vault holds the Anthropic key as AI_CLI_TOKEN, but the routine declares
	// the requirement as api_key (the placeholder-mode type).
	seedVaultCredentialFull(t, h.db, wsID, userID, "cred_oat", "AI_CLI_TOKEN", "ANTHROPIC", "")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_oat", "oatp", credGateDef("api_key"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "oatp"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("run blocked, but an Anthropic AI_CLI_TOKEN satisfies an api_key requirement; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCredentialGate_AnthropicKeyPinnedToOtherCrewSatisfiesRun pins the second
// half of the divergence: the LLM runner resolves an Anthropic key workspace-
// wide (no crew filter), so a key pinned to a non-author crew still powers the
// run. The gate's exact-type probe is author-crew-scoped and would miss it —
// the provider-aware fallback must not.
func TestCredentialGate_AnthropicKeyPinnedToOtherCrewSatisfiesRun(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncKeyCredGate)
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	authorCrew := seedCrewRow(t, h.db, "crew_author", wsID, "Ops", "ops")
	otherCrew := seedCrewRow(t, h.db, "crew_other", wsID, "Payments", "payments")
	_ = seedAgentRow(t, h.db, "ag_pin", wsID, authorCrew, "Morgan", "morgan", "LEAD")
	// Anthropic API_KEY pinned to a DIFFERENT crew than the routine's author.
	seedVaultCredentialFull(t, h.db, wsID, userID, "cred_pin", "API_KEY", "ANTHROPIC", otherCrew)
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_pin", "pinp", credGateDef("api_key"), authorCrew)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "pinp"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("run blocked, but a workspace-wide Anthropic key satisfies the LLM runner; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCredentialGate_NonAnthropicTypeStillBlocks guards against the fallback
// over-reaching: a non-LLM requirement (stripe) the vault cannot satisfy must
// still 422, even when an Anthropic key is present. The provider-aware path
// only rescues api_key / ai_cli_token requirements.
func TestCredentialGate_NonAnthropicTypeStillBlocks(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_nab", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_nab", wsID, crewID, "Morgan", "morgan", "LEAD")
	seedVaultCredentialFull(t, h.db, wsID, userID, "cred_nab", "AI_CLI_TOKEN", "ANTHROPIC", "")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_nab", "nabp", credGateDef("stripe"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "nabp"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (stripe is unsatisfiable); body=%s", rr.Code, rr.Body.String())
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times; a blocked run must not execute", runner.calls)
	}
}

func TestCredentialGate_BlocksWhenCredentialMissing(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_cblk", wsID, "Payments", "payments")
	_ = seedAgentRow(t, h.db, "ag_cblk", wsID, crewID, "Eva", "eva", "LEAD")
	// Vault holds no stripe credential → declaring it must block.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_cblk", "cblk", credGateDef("stripe"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "cblk"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var prob struct {
		Detail             string   `json:"detail"`
		MissingCredentials []string `json:"missing_credentials"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(prob.MissingCredentials) != 1 || prob.MissingCredentials[0] != "stripe" {
		t.Fatalf("missing_credentials = %#v, want [stripe]", prob.MissingCredentials)
	}
	if !strings.Contains(prob.Detail, "stripe") {
		t.Errorf("detail = %q, want mention of stripe", prob.Detail)
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times; a blocked run must not execute", runner.calls)
	}
}

func TestCredentialGate_PassesWhenCredentialPresent(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncKeyCredGate)
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_cok", wsID, "Payments", "payments")
	_ = seedAgentRow(t, h.db, "ag_cok", wsID, crewID, "Eva", "eva", "LEAD")
	seedVaultCredential(t, h.db, wsID, userID, "cred_stripe", "STRIPE") // vault type stored uppercase
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_cok", "cokp", credGateDef("stripe"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "cokp"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("run blocked but credential is present; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// testEncKeyCredGate is a fixed dev cipher key (not a secret) — some run
// paths touch encryption on start; the gate itself never decrypts.
const testEncKeyCredGate = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow
