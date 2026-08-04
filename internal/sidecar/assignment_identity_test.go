package sidecar

// Who is dispatching? (#1754)
//
// /assign used to ask nobody. The doc comment said "from lead agents" and the
// code checked the target slug and crew membership — never the caller — so any
// process in the crew container that could reach 127.0.0.1:9119 dispatched
// work, with no Authorization header at all.
//
// That is now the same #812 identity contract every other attributed route
// applies, and it is load-bearing rather than tidy: the depth cap in crewshipd
// measures the tree from the assignment the CALLER is executing, so it needs
// the caller, not the agent that happened to boot the shared per-crew sidecar.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// forwardCapture stands in for crewshipd and records the body it was handed.
func forwardCapture(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"assignment_id":"a-1","status":"PENDING"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func assignAs(t *testing.T, srv *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/assign",
		strings.NewReader(`{"target":"viktor","task":"write tests"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.handleAssign(w, req)
	return w
}

// The dispatch is attributed to the agent whose token was presented — not to
// the agent the sidecar was booted for. Without this a sub-agent's dispatch
// files under its lead, and every hop measures the LEAD's depth, i.e. 1.
func TestHandleAssign_AttributesToTheActingAgentNotTheBootAgent(t *testing.T) {
	mock, forwarded := forwardCapture(t)
	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "internal", CrewID: "crew-1", WorkspaceID: "ws-1",
		ChatID: "chat-1", AgentID: "lead-1", AgentSlug: "lead", AgentToken: "tok-lead",
	}, []CrewMember{
		{ID: "sub-1", Slug: "sub", Name: "Sub", AuthToken: "tok-sub"},
		{ID: "vik-1", Slug: "viktor", Name: "Viktor", AuthToken: "tok-viktor"},
	})

	if w := assignAs(t, srv, "tok-sub"); w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for an authenticated sub-agent, got %d (%s)", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(*forwarded), &body); err != nil {
		t.Fatalf("decode forwarded body %q: %v", *forwarded, err)
	}
	if body["actor_agent_id"] != "sub-1" {
		t.Errorf("actor_agent_id = %q, want sub-1 (the token holder, not the boot agent lead-1)", body["actor_agent_id"])
	}
}

// A token that matches no crew member is a forgery.
func TestHandleAssign_ForgedTokenIsRefused(t *testing.T) {
	mock, _ := forwardCapture(t)
	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "internal", CrewID: "crew-1", WorkspaceID: "ws-1",
		ChatID: "chat-1", AgentID: "lead-1", AgentSlug: "lead", AgentToken: "tok-lead",
	}, []CrewMember{{ID: "vik-1", Slug: "viktor", Name: "Viktor", AuthToken: "tok-viktor"}})

	if w := assignAs(t, srv, "tok-made-up"); w.Code != http.StatusForbidden {
		t.Fatalf("forged token: expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

// Dropping the header is not a way back to the boot agent's identity. On a crew
// where tokens ARE issued, an unauthenticated /assign is a downgrade attempt —
// and, for the caps, a way to have your dispatch counted against someone else's
// position in the tree.
func TestHandleAssign_TokenlessDowngradeIsRefused(t *testing.T) {
	mock, _ := forwardCapture(t)
	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "internal", CrewID: "crew-1", WorkspaceID: "ws-1",
		ChatID: "chat-1", AgentID: "lead-1", AgentSlug: "lead", AgentToken: "tok-lead",
	}, []CrewMember{{ID: "vik-1", Slug: "viktor", Name: "Viktor", AuthToken: "tok-viktor"}})

	if w := assignAs(t, srv, ""); w.Code != http.StatusForbidden {
		t.Fatalf("token-less call on a token-issuing crew: expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

// A genuinely token-less deployment (no per-agent tokens minted anywhere) keeps
// working off the boot identity, exactly as the other #812 routes do. The
// fallback is for old sidecars, not for new callers that skipped the header.
func TestHandleAssign_LegacyTokenlessCrewStillDispatches(t *testing.T) {
	mock, forwarded := forwardCapture(t)
	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "internal", CrewID: "crew-1", WorkspaceID: "ws-1",
		ChatID: "chat-1", AgentID: "lead-1", AgentSlug: "lead",
	}, []CrewMember{{ID: "vik-1", Slug: "viktor", Name: "Viktor"}})

	if w := assignAs(t, srv, ""); w.Code != http.StatusCreated {
		t.Fatalf("legacy token-less crew: expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(*forwarded), &body); err != nil {
		t.Fatalf("decode forwarded body %q: %v", *forwarded, err)
	}
	if body["actor_agent_id"] != "lead-1" {
		t.Errorf("actor_agent_id = %q, want the boot agent lead-1 on a token-less crew", body["actor_agent_id"])
	}
}
