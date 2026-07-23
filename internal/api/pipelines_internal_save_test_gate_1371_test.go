package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #1371 — the agent-authored InternalSave path must be no weaker than the
// interactive user save path. The user path (pipelines_crud.go) does NOT
// trust the forgeable last_test_run_passed body field; it only clears the
// store test-gate against an HMAC save_token minted by /test_run. These
// tests prove InternalSave now requires the same proof-of-test.

const testSaveTokenSecret1371 = "internal-save-token-secret-1371"

// signInternalSaveTokenForTest mints the HMAC save_token an InternalSave body
// must carry to clear the #1371 store test-gate. `def` must be the exact
// (compact, whitespace-free) definition bytes the request body embeds, so the
// hash the token is signed over matches what the handler re-derives.
func signInternalSaveTokenForTest(secret []byte, wsID, crewID, def string) string {
	return signSaveToken(secret, wsID, definitionHashHex([]byte(def)),
		internalSaveTokenSubject(crewID), time.Now())
}

// buildInternalSaveHandler wires a handler with the save_token signing
// secret enabled (mirrors cmd_start.go SetSaveTokenSecret) plus a crew +
// agent the validator can resolve the referenced agent_slug against.
func buildInternalSaveHandler1371(t *testing.T) (*PipelineHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-1371", wsID, "Eng", "eng")
	seedAgentRow(t, db, "a-1371", wsID, crewID, "Lead", "agent_lead", "LEAD")

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	h.SetSaveTokenSecret([]byte(testSaveTokenSecret1371))
	return h, wsID, crewID
}

// def1371 is a compact (whitespace-free) DSL so the json.RawMessage the
// handler decodes is byte-identical to what we hash when minting a token.
const def1371 = `{"name":"my-pipe","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`

func countPipelines1371(t *testing.T, h *PipelineHandler, wsID, slug string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(
		"SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = ?", wsID, slug,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestPipelineInternalSave_ForgedTestGate_Rejected_1371 is the reproducing
// test: an agent that forges last_test_run_passed=true + a fresh
// last_test_run_at, but presents NO save_token, must be rejected by the
// store gate (422) and must NOT land a pipeline row. On main this returned
// 201 and activated the routine — the bypass this issue closes.
func TestPipelineInternalSave_ForgedTestGate_Rejected_1371(t *testing.T) {
	h, wsID, crewID := buildInternalSaveHandler1371(t)

	fresh := time.Now().UTC().Format(time.RFC3339)
	body := `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe",` +
		`"author_crew_id":"` + crewID + `",` +
		`"last_test_run_passed":true,"last_test_run_at":"` + fresh + `",` +
		`"definition":` + def1371 + `}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (forged test-gate must be rejected); body=%s", rr.Code, rr.Body.String())
	}
	if n := countPipelines1371(t, h, wsID, "my-pipe"); n != 0 {
		t.Fatalf("pipeline rows=%d want 0 (forged save must not persist)", n)
	}
}

// TestPipelineInternalSave_ValidSaveToken_Activates_1371 is the positive
// path: a save_token minted (as InternalTestRun does) over the SAME
// definition_hash + authoring crew clears the gate, the routine activates.
func TestPipelineInternalSave_ValidSaveToken_Activates_1371(t *testing.T) {
	h, wsID, crewID := buildInternalSaveHandler1371(t)

	defHash := pipeline.DefinitionHash([]byte(def1371))
	token := signSaveToken([]byte(testSaveTokenSecret1371), wsID, defHash,
		internalSaveTokenSubject(crewID), time.Now())

	body := `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe",` +
		`"author_crew_id":"` + crewID + `",` +
		`"save_token":"` + token + `",` +
		`"definition":` + def1371 + `}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "active" {
		t.Errorf("status=%v want active (safe routine, gate cleared)", resp["status"])
	}
	if n := countPipelines1371(t, h, wsID, "my-pipe"); n != 1 {
		t.Errorf("pipeline rows=%d want 1", n)
	}
}

// TestPipelineInternalSave_SaveTokenWrongDefinition_Rejected_1371 proves the
// token is bound to the definition_hash: a token minted for one definition
// cannot clear the gate for a different (tampered) definition.
func TestPipelineInternalSave_SaveTokenWrongDefinition_Rejected_1371(t *testing.T) {
	h, wsID, crewID := buildInternalSaveHandler1371(t)

	otherHash := pipeline.DefinitionHash([]byte(`{"name":"other","steps":[]}`))
	token := signSaveToken([]byte(testSaveTokenSecret1371), wsID, otherHash,
		internalSaveTokenSubject(crewID), time.Now())

	body := `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe",` +
		`"author_crew_id":"` + crewID + `",` +
		`"save_token":"` + token + `",` +
		`"definition":` + def1371 + `}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (token for a different definition must not verify); body=%s", rr.Code, rr.Body.String())
	}
	if n := countPipelines1371(t, h, wsID, "my-pipe"); n != 0 {
		t.Fatalf("pipeline rows=%d want 0", n)
	}
}
