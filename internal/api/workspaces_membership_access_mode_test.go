package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The members list carries each member's access mode (#2878) so Settings ›
// Members and `workspace member list` can mark restricted members without one
// policy request per row. The mode is part of the member access policy, so the
// list exposes it under the same gate as GET /members/{id}/access: a trusted
// OWNER/ADMIN whose token may administer the workspace.
func TestListMembersAccessModeFollowsPolicyReadGate(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	covWMSeedMember(t, h, "am-admin", "am-admin@example.com", wsID, "am-m-admin", "ADMIN")
	covWMSeedMember(t, h, "am-member", "am-member@example.com", wsID, "am-m-member", "MEMBER")
	covWMSeedMember(t, h, "am-restricted", "am-restricted@example.com", wsID, "am-m-restricted", "MEMBER")
	covWMSeedMember(t, h, "am-radmin", "am-radmin@example.com", wsID, "am-m-radmin", "ADMIN")
	for _, id := range []string{"am-m-restricted", "am-m-radmin"} {
		execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode = 'restricted' WHERE id = ?`, id)
	}

	tests := []struct {
		name   string
		caller string
		role   string
		scopes stringSet
		sees   bool
	}{
		{name: "owner session", caller: ownerID, role: "OWNER", sees: true},
		{name: "trusted admin session", caller: "am-admin", role: "ADMIN", sees: true},
		{name: "owner token with workspace admin scope", caller: ownerID, role: "OWNER", scopes: stringSet{"workspace:admin": {}}, sees: true},
		{name: "owner token without workspace admin scope", caller: ownerID, role: "OWNER", scopes: stringSet{"agents:read": {}}},
		{name: "member", caller: "am-member", role: "MEMBER"},
		{name: "restricted admin", caller: "am-radmin", role: "ADMIN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/members", nil), tc.caller, wsID, tc.role)
			if tc.scopes != nil {
				req = req.WithContext(context.WithValue(req.Context(), ctxTokenScopes, tc.scopes))
			}
			rr := httptest.NewRecorder()
			h.ListMembers(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(rows) != 5 {
				t.Fatalf("members = %d, want 5", len(rows))
			}
			for _, row := range rows {
				mode, present := row["access_mode"]
				if !tc.sees {
					if present {
						t.Errorf("member %v: access_mode %v leaked to caller without policy read", row["id"], mode)
					}
					continue
				}
				want := "trusted"
				if row["id"] == "am-m-restricted" || row["id"] == "am-m-radmin" {
					want = "restricted"
				}
				if mode != want {
					t.Errorf("member %v: access_mode = %v, want %s", row["id"], mode, want)
				}
			}
		})
	}
}
