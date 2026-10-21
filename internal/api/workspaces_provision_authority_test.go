package api

import (
	"net/http"
	"testing"
)

func TestProvisionAdminRequiresWorkspaceOwner(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new_account"
		if existing {
			name = "existing_account"
		}
		t.Run(name, func(t *testing.T) {
			h, actor, workspace := provisionRig(t)
			if existing {
				seedOtherUser(t, h, "existing-admin-target", "target@example.test")
				if _, err := h.db.Exec(`INSERT INTO verification_tokens(identifier,token,expires,purpose) VALUES('target@example.test','preserve-setup','2099-01-01T00:00:00Z','account_setup')`); err != nil {
					t.Fatal(err)
				}
			}
			rr := provisionReq(t, h, actor, workspace, "ADMIN", `{"email":"target@example.test","role":"ADMIN"}`)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("workspace ADMIN provisioning ADMIN: status %d, want 403", rr.Code)
			}
			var accounts, memberships, setupTokens int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='target@example.test'`).Scan(&accounts); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members wm JOIN users u ON u.id=wm.user_id WHERE wm.workspace_id=? AND u.email='target@example.test'`, workspace).Scan(&memberships); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier='target@example.test'`).Scan(&setupTokens); err != nil {
				t.Fatal(err)
			}
			want := 0
			if existing {
				want = 1
				var token string
				if err := h.db.QueryRow(`SELECT token FROM verification_tokens WHERE identifier='target@example.test'`).Scan(&token); err != nil || token != "preserve-setup" {
					t.Fatalf("existing setup changed: token=%q err=%v", token, err)
				}
			}
			if accounts != want || setupTokens != want || memberships != 0 {
				t.Fatalf("denied provisioning changed state: accounts=%d memberships=%d setup=%d", accounts, memberships, setupTokens)
			}
		})
	}
}

func TestProvisionRoleAuthorityPositiveControls(t *testing.T) {
	for _, tc := range []struct{ actor, target string }{
		{"OWNER", "ADMIN"}, {"ADMIN", "MANAGER"}, {"ADMIN", "MEMBER"}, {"ADMIN", "VIEWER"},
	} {
		t.Run(tc.actor+"_creates_"+tc.target, func(t *testing.T) {
			h, actor, workspace := provisionRig(t)
			rr := provisionReq(t, h, actor, workspace, tc.actor, `{"email":"positive@example.test","role":"`+tc.target+`"}`)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status %d, want 201: %s", rr.Code, rr.Body.String())
			}
			out := decodeProvision(t, rr)
			if out.Role != tc.target || out.SetupURL == "" {
				t.Fatalf("unexpected provisioned result: role=%q setup present=%v", out.Role, out.SetupURL != "")
			}
		})
	}
}
