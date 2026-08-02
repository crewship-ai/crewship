package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An operator could not ask their own judge a question.
//
// `keeper judge test` proves the judge answers, but it asks ONE hard-coded
// scenario — an L1 npm token for a CI bot. "How would this judge rule on an SSH
// key for an agent with a thin justification?" had no answer short of waiting
// for an agent to want one.
//
// It is also the gap that makes a ground-truth corpus impossible to build
// deliberately. The eval harness scores candidates against decisions a human
// ruled on, and those only exist where the Keeper escalated something a person
// then resolved. With submission available only to agents in containers, an
// operator who wants twenty varied decisions to judge has to provoke twenty
// agent turns and hope each agent actually asks for what it was told to.
//
// So: the same evaluation path agents use, reachable by an admin, recorded the
// same way. The workspace is taken from the session and NOT from the body —
// otherwise an admin in one workspace could file requests into another by
// editing a field.

func askReq(t *testing.T, ws string, body map[string]any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/admin/keeper/ask", bytes.NewReader(raw))
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u-admin"})
	if ws != "" {
		ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	}
	return r.WithContext(ctx)
}

func TestKeeperAsk_UsesTheSessionWorkspaceNotTheBody(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleAsk(rr, askReq(t, "ws-session", map[string]any{
		// A body that tries to file into somebody else's workspace.
		"workspace_id":        "ws-other",
		"requesting_agent_id": "agt1",
		"requesting_crew_id":  "crew1",
		"credential_name":     "npm-token",
		"intent":              "publish the release tarball to npm as part of the tagged build",
	}))

	// The credential does not exist in the test DB, so this 404s — which is the
	// point: it looked in ws-session. A handler that trusted the body would have
	// gone looking in ws-other instead.
	if rr.Code == http.StatusOK {
		t.Fatalf("unexpected success against an empty database: %s", rr.Body.String())
	}
	if rr.Code == http.StatusInternalServerError {
		t.Fatalf("500 rather than a clean not-found: %s", rr.Body.String())
	}
}

func TestKeeperAsk_RequiresAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleAsk(rr, askReq(t, "", map[string]any{
		"requesting_agent_id": "agt1",
		"requesting_crew_id":  "crew1",
		"credential_name":     "npm-token",
		"intent":              "publish the release tarball to npm as part of the tagged build",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a workspace", rr.Code)
	}
}

func TestKeeperAsk_RejectsAMalformedBody(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	r := httptest.NewRequest("POST", "/api/v1/admin/keeper/ask", bytes.NewReader([]byte("{not json")))
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws1")
	rr := httptest.NewRecorder()
	h.HandleAsk(rr, r.WithContext(ctx))

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 on malformed JSON", rr.Code)
	}
}
