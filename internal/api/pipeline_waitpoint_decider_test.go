package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// PRD §18 scenario 10 (B14, #2388), at the handler: a peer agent's "GO"
// cannot satisfy a waitpoint. These drive ApproveWaitpoint against the
// REAL SQLWaitpointStore so the door's refusal, the untouched row and the
// audit record are all observed, not stubbed.

func seedPendingWaitpoint(t *testing.T, h *PipelineHandler, wsID string) (*pipeline.SQLWaitpointStore, string) {
	t.Helper()
	store := pipeline.NewSQLWaitpointStore(h.db)
	t.Cleanup(store.Close)
	h.SetWaitpointStore(store)
	tok, err := store.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{
		WorkspaceID: wsID, PipelineRunID: "run-b14", StepID: "gate", Prompt: "Ship it?", TimeoutSec: 3600,
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	return store, tok
}

func waitpointRow(t *testing.T, h *PipelineHandler, tok string) (status, decidedBy string) {
	t.Helper()
	var by *string
	if err := h.db.QueryRow(`SELECT status, decided_by_user_id FROM pipeline_waitpoints WHERE token = ?`, tok).Scan(&status, &by); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if by != nil {
		decidedBy = *by
	}
	return status, decidedBy
}

func refusalCount(t *testing.T, h *PipelineHandler, tok string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND entity_type = 'waitpoint' AND entity_id = ?`,
		pipeline.AuditActionWaitpointDecisionRefused, tok).Scan(&n); err != nil {
		t.Fatalf("count refusals: %v", err)
	}
	return n
}

// withInternalCrewToken shapes the context requireInternal leaves behind
// for a crew-bound X-Internal-Token: the workspace and crew the token is
// bound to, and no user. This is what a peer agent's sidecar presents.
func withInternalCrewToken(req *http.Request, wsID, crewID string) *http.Request {
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "OWNER") // even a permissive role does not make it a person
	ctx = context.WithValue(ctx, ctxInternalTokenWS, wsID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, crewID)
	return req.WithContext(ctx)
}

func TestApproveWaitpoint_PeerAgentGO_RefusedAndRecorded(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, wsID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true,"comment":"GO"}`))
	req.SetPathValue("token", tok)
	req = withInternalCrewToken(req, wsID, "crew-peer")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "waitpoint_decider_not_allowed") {
		t.Errorf("body does not name the refusal: %s", rr.Body.String())
	}
	if status, by := waitpointRow(t, h, tok); status != "pending" || by != "" {
		t.Fatalf("waitpoint after peer GO = (%s, %q), want (pending, \"\")", status, by)
	}
	if n := refusalCount(t, h, tok); n != 1 {
		t.Fatalf("refusal records = %d, want 1", n)
	}
	var meta string
	if err := h.db.QueryRow(`SELECT metadata FROM audit_logs WHERE action = ? AND entity_id = ?`,
		pipeline.AuditActionWaitpointDecisionRefused, tok).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, `"actor_id":"crew:crew-peer"`) || !strings.Contains(meta, `"actor_kind":"agent"`) {
		t.Errorf("refusal metadata does not name the peer agent: %s", meta)
	}
}

func TestApproveWaitpoint_NoPrincipal_RefusedFailClosed(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, wsID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	req.SetPathValue("token", tok)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER")) // workspace + role, no user
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if status, _ := waitpointRow(t, h, tok); status != "pending" {
		t.Fatalf("status = %s, want pending", status)
	}
	if n := refusalCount(t, h, tok); n != 1 {
		t.Fatalf("refusal records = %d, want 1", n)
	}
}

func TestApproveWaitpoint_PersonDecides_AfterPeerWasRefused(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, wsID)

	peer := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	peer.SetPathValue("token", tok)
	peer = withInternalCrewToken(peer, wsID, "crew-peer")
	h.ApproveWaitpoint(httptest.NewRecorder(), peer)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true,"comment":"LGTM"}`))
	req.SetPathValue("token", tok)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	status, by := waitpointRow(t, h, tok)
	if status != "approved" || by != userID {
		t.Fatalf("waitpoint = (%s, %q), want (approved, %q)", status, by, userID)
	}
	if n := refusalCount(t, h, tok); n != 1 {
		t.Fatalf("refusal records = %d, want exactly the peer's 1", n)
	}
}

func TestCompleteWaitpointToken_ExternalHolderDecides(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, wsID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	req.SetPathValue("token", tok)
	rr := httptest.NewRecorder()
	h.CompleteWaitpointToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if status, by := waitpointRow(t, h, tok); status != "approved" || by != "external-callback" {
		t.Fatalf("waitpoint = (%s, %q), want (approved, external-callback)", status, by)
	}
	if n := refusalCount(t, h, tok); n != 0 {
		t.Fatalf("refusal records = %d, want 0", n)
	}
}

func TestWaitpointDeciderFromRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func(context.Context) context.Context
		want pipeline.WaitpointDecider
	}{
		{"session or CLI-token user", func(ctx context.Context) context.Context {
			return withUser(ctx, &AuthUser{ID: "usr-1"})
		}, pipeline.WaitpointDecider{Kind: pipeline.DeciderUser, ID: "usr-1"}},
		{"crew-bound internal token is an agent", func(ctx context.Context) context.Context {
			ctx = context.WithValue(ctx, ctxInternalTokenWS, "ws-1")
			return context.WithValue(ctx, ctxInternalTokenCrew, "crew-1")
		}, pipeline.WaitpointDecider{Kind: pipeline.DeciderAgent, ID: "crew:crew-1"}},
		{"workspace-bound internal token is an agent", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ctxInternalTokenWS, "ws-1")
		}, pipeline.WaitpointDecider{Kind: pipeline.DeciderAgent, ID: "workspace-token:ws-1"}},
		{"user wins over a stray internal binding", func(ctx context.Context) context.Context {
			ctx = withUser(ctx, &AuthUser{ID: "usr-2"})
			return context.WithValue(ctx, ctxInternalTokenCrew, "crew-1")
		}, pipeline.WaitpointDecider{Kind: pipeline.DeciderUser, ID: "usr-2"}},
		{"nothing is nobody", func(ctx context.Context) context.Context { return ctx }, pipeline.WaitpointDecider{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", nil)
			req = req.WithContext(tc.ctx(req.Context()))
			if got := waitpointDeciderFromRequest(req); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
