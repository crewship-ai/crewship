// Coverage for the PUBLIC pipelines test_run surface.
//
// /test_run is the JWT-authed draft-validation gate behind the UI "Test run"
// button: it dry-run-validates an UNSAVED definition (parse + Validate +
// integration/resource gates + ModeDryRun — no agent invocation, since you
// cannot run an agent "dry") and, on success, mints the HMAC save_token that
// Save verifies. Without a minting endpoint the save test-gate degrades to
// self-asserted body fields any client can forge, so these tests pin both that
// the route is wired AND that it issues a verifiable token.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// TestPipelineTestRunRoute_Registered confirms the public route resolves to the
// TestRun handler. The Router wires the handler with a nil runner (the
// orchestrator attaches it post-construction), so a reached handler answers 503
// ("runner not wired") — which is exactly the proof the route exists: a missing
// route would surface as 404/405 from net/http before any handler runs.
func TestPipelineTestRunRoute_Registered(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("auth.NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	body := `{"definition":{"name":"x","steps":[]},"author_crew_id":"crew_a"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_1/pipelines/test_run", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound || rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST .../pipelines/test_run = %d; the public TestRun route must be registered (must NOT be 404/405); body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestTestRunHandler_MintsVerifiableSaveToken drives the handler directly with a
// signing secret + a wired (dry-run never-invoked) runner and asserts it returns
// a save_token that verifies for the same (workspace, definition_hash, user).
func TestTestRunHandler_MintsVerifiableSaveToken(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "unused-in-dry-run"}, nil)
	secret := []byte("test-secret-32-bytes-long-padxxx")
	h.SetSaveTokenSecret(secret)

	const userID = "user_tr"
	const wsID = "ws_smoke"
	def := `{"name":"tr-routine","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	body := `{"definition":` + def + `,"sample_inputs":{}}`

	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = req.WithContext(context.WithValue(withWorkspace(req.Context(), wsID, "MANAGER"), ctxUser, &AuthUser{ID: userID}))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	h.TestRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status    string `json:"status"`
		SaveToken string `json:"save_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "DRY_RUN_OK" {
		t.Errorf("status = %q, want DRY_RUN_OK", out.Status)
	}
	if out.SaveToken == "" {
		t.Fatal("expected a minted save_token, got empty")
	}
	// The token must verify for THIS definition + user — the proof Save checks.
	if err := verifySaveToken(secret, out.SaveToken, wsID, definitionHashHex([]byte(def)), userID); err != nil {
		t.Errorf("minted save_token failed verification: %v", err)
	}
	// And it must NOT verify for a different definition (binding is real).
	if err := verifySaveToken(secret, out.SaveToken, wsID, definitionHashHex([]byte(`{"name":"other"}`)), userID); err == nil {
		t.Error("save_token verified for a different definition_hash — binding is broken")
	}
}
