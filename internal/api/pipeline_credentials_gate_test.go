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
