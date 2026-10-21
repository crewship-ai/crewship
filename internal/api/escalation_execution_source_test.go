package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestEscalationPersistsOnlyVerifiedExecutionSource(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewQueryHandler(f.h.db, nil, nil, "execution-source-test", newTestLogger())
	key := internaltoken.DeriveAgentRunKey(h.internalToken, f.workspace, f.crew)
	call := func(run, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{
			"reason": "Need information", "crew_id": f.crew, "workspace_id": f.workspace,
			"source_run_id": run, "actor_agent_id": f.agent, "chat_id": f.chat,
		}))
		req.Header.Set("X-Crewship-Run-Token", token)
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.workspace))
		rr := httptest.NewRecorder()
		h.CreateEscalation(rr, req)
		return rr
	}
	rr := call(f.run, internaltoken.DeriveAgentRunToken(key, f.workspace, f.agent, f.run))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		ID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var run string
	if err := h.db.QueryRow(`SELECT source_run_id FROM escalations WHERE id=?`, body.ID).Scan(&run); err != nil || run != f.run {
		t.Fatalf("stored source=%q err=%v", run, err)
	}
	rr = call("foreign-run", internaltoken.DeriveAgentRunToken(key, f.workspace, f.agent, f.run))
	if rr.Code < 400 {
		t.Fatalf("mismatched source accepted: %d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM escalations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestEscalationLegacySourceStaysUnanchoredAndInboxResolvable(t *testing.T) {
	h, user, workspace, crew, agent := covEscFixture(t)
	seedChat(t, h, "source-legacy-chat", agent, workspace)
	rr := createEsc(h, workspace, map[string]string{"from_slug": "covesc-ag", "reason": "Legacy help", "crew_id": crew, "workspace_id": workspace, "chat_id": "source-legacy-chat"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("legacy create: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		ID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := h.db.QueryRow(`SELECT source_run_id IS NOT NULL FROM escalations WHERE id=?`, body.ID).Scan(&present); err != nil || present {
		t.Fatalf("legacy inferred source: %v %v", present, err)
	}
	rr = covEscResolve(h, user, workspace, body.ID, map[string]string{"resolution": "Existing answer", "action": "approve"})
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy resolve: %d %s", rr.Code, rr.Body.String())
	}
}
