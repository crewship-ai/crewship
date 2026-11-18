package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKeeperApprovalSpendRollsBackWithPendingInsertFailure(t *testing.T) {
	for _, kind := range []string{"access", "execute"} {
		t.Run(kind, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, credential := seedL4Fixture(t, db)
			h, _ := newL4Handler(t, db, nil, nil)
			intent := "Verify this isolated synthetic fixture account during the explicitly approved regression change window."
			call := func(approval string) *httptest.ResponseRecorder {
				if kind == "access" {
					return doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, CredentialID: credential, Intent: intent, ApprovalRequestID: approval})
				}
				return doKeeperExecute(h, keeperExecuteBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, CredentialID: credential, Intent: intent, Command: "true", ContainerID: "test-container", ApprovalRequestID: approval})
			}
			initial := call("")
			var result struct {
				RequestID string `json:"request_id"`
				Decision  string `json:"decision"`
			}
			if err := json.Unmarshal(initial.Body.Bytes(), &result); err != nil || initial.Code != http.StatusOK || result.Decision != "ESCALATE" {
				t.Fatalf("fixture escalation failed: %d %s %v", initial.Code, result.Decision, err)
			}
			approval := result.RequestID
			var owner string
			if err := db.QueryRow(`SELECT created_by FROM credentials WHERE id=?`, credential).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if resolved := resolveDecision(t, h, ws, approval, "ALLOW", owner); resolved.Code != http.StatusOK {
				t.Fatalf("fixture resolve failed: %d", resolved.Code)
			}
			execOrFatal(t, db, `CREATE TRIGGER fixture_pending_failure BEFORE INSERT ON keeper_requests WHEN NEW.approval_consumed_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'fixture pending insertion failed'); END`)
			if out := call(approval); out.Code != http.StatusInternalServerError {
				t.Errorf("pending failure returned %d", out.Code)
			}
			var consumed bool
			if err := db.QueryRow(`SELECT approval_consumed_at IS NOT NULL FROM keeper_requests WHERE id=?`, approval).Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			if consumed {
				t.Error("pending insert failure committed the approval spend")
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&n); err != nil || n != 1 {
				t.Errorf("failed insertion retained request: %d %v", n, err)
			}
		})
	}
}
