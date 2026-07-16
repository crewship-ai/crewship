package api

// Tier-alignment regression tests for the OAuth connect flow (#1034).
//
// Initiate / Exchange / Loopback used to gate on canRole("manage")
// (ADMIN+) while POST /credentials — which creates the very OAUTH2 row
// the flow authorizes — gates on the layered MANAGER+-or-
// credential.create check. That mismatch broke the /credentials OAuth
// entry point for a MANAGER: the create step succeeded, the authorize
// step 403'd. These tests pin the aligned behaviour: MANAGER passes by
// role, MEMBER passes with an explicit credential.create grant, MEMBER
// without the grant is denied.
//
// Same boundary-probe strategy as public_capability_gate_test.go: send
// a body that fails the first post-gate validation so a granted request
// surfaces as 400 and a denied one as 403 — proving the gate decision
// without stubbing the rest of the flow.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthFlowCapabilityGate(t *testing.T) {
	endpoints := []struct {
		name    string
		handler func(h *OAuthHandler) http.HandlerFunc
	}{
		{"initiate", func(h *OAuthHandler) http.HandlerFunc { return h.Initiate }},
		{"exchange", func(h *OAuthHandler) http.HandlerFunc { return h.Exchange }},
		{"loopback", func(h *OAuthHandler) http.HandlerFunc { return h.Loopback }},
	}

	for _, ep := range endpoints {
		for _, tc := range publicGateCommonCases(CapabilityCredentialCreate) {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				h, db := newOAuthHandler(t)

				callerID := "oauth-gate-" + ep.name + "-" + strings.ReplaceAll(tc.name, " ", "-")
				wsID := "ws-oauth-gate"
				if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'G', 'g-oauth-gate')`, wsID); err != nil {
					t.Fatalf("seed workspace: %v", err)
				}
				if tc.memberCaps != "" {
					if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'T')`,
						callerID, callerID+"@x"); err != nil {
						t.Fatalf("seed user: %v", err)
					}
					if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
						"m-"+callerID, wsID, callerID, tc.role, tc.memberCaps); err != nil {
						t.Fatalf("seed member: %v", err)
					}
					InvalidateCapabilityCache(wsID, callerID)
				}

				// Empty JSON body: gate-pass surfaces the "credential_id is
				// required" 400; gate-deny short-circuits at 403.
				req := httptest.NewRequest("POST", "/x", strings.NewReader(`{}`))
				ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
				ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID})
				ctx = context.WithValue(ctx, ctxRole, tc.role)
				req = req.WithContext(ctx)

				rr := httptest.NewRecorder()
				ep.handler(h)(rr, req)

				if rr.Code != tc.wantStatus {
					t.Errorf("%s: status = %d, want %d (%s) — body: %s",
						ep.name, rr.Code, tc.wantStatus, tc.description, rr.Body.String())
				}
			})
		}
	}
}
