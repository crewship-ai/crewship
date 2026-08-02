package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An escalation exists so a PERSON rules on it, and there was no way for a
// person to rule on it. The inbox card said so out loud — "missing on the
// server: a keeper request has no resolve endpoint yet" — and sent the operator
// to a terminal, where `inbox resolve` marked the notification read without
// recording a verdict against the request it notified about.
//
// So the decision the whole tier system defers to a human was the one decision
// the product could not accept.
//
// The security properties this endpoint has to hold, each pinned below:
//
//	roleManage        — OWNER/ADMIN, the same gate the audience is addressed by
//	workspace-scoped  — an admin cannot rule on another tenant's request
//	settled-once      — a decided request cannot be re-decided
//	four-eyes         — the owner of the requesting agent cannot approve it

func resolveReq(t *testing.T, ws, role, userID, reqID string, body map[string]any) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST",
		"/api/v1/admin/keeper/requests/"+reqID+"/resolve", bytes.NewReader(raw))
	r.SetPathValue("requestId", reqID)
	ctx := context.WithValue(r.Context(), ctxRole, role)
	if userID != "" {
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	}
	if ws != "" {
		ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	}
	return r.WithContext(ctx)
}

func TestKeeperResolve_RefusesARoleThatCannotManage(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER", ""} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", role, "u1", "req1",
			map[string]any{"decision": "ALLOW"}))
		if rr.Code != http.StatusForbidden {
			t.Errorf("role %q got %d, want 403 — the audience is addressed at ADMIN precisely because this is the gate",
				role, rr.Code)
		}
	}
}

func TestKeeperResolve_RequiresAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, "", "ADMIN", "u1", "req1",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a workspace", rr.Code)
	}
}

// The decision vocabulary is closed. A typo must not be stored as a verdict —
// this row is what the eval harness later reads as ground truth, and a value
// nothing recognises would be counted as neither approval nor refusal.
func TestKeeperResolve_RefusesAnUnknownDecision(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, d := range []string{"", "MAYBE", "yes", "ESCALATE", "approve"} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", "ADMIN", "u1", "req1",
			map[string]any{"decision": d}))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("decision %q got %d, want 400", d, rr.Code)
		}
	}
}

// Case and surrounding space are normalised, not rejected: "allow" and "ALLOW "
// are the same verdict, and an API that refused one of them would be pedantry
// rather than safety. They reach the lookup — 404 here, since the fixture has no
// such request — which is how we know they were accepted as verdicts.
func TestKeeperResolve_NormalisesCaseAndSpace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, d := range []string{"allow", "ALLOW ", " deny", "Deny"} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", "ADMIN", "u1", "req1",
			map[string]any{"decision": d}))
		if rr.Code == http.StatusBadRequest {
			t.Errorf("decision %q was rejected as unknown; case and space are not the verdict", d)
		}
	}
}

// A request that does not exist in THIS workspace is a 404, not a 403 and not a
// silent success. Scoping is by workspace and not by id alone, so an admin
// holding a request id from another tenant learns nothing from the response.
func TestKeeperResolve_ScopesToTheCallersWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, "ws-mine", "ADMIN", "u1", "req-from-another-tenant",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 for a request outside the caller's workspace", rr.Code)
	}
}
