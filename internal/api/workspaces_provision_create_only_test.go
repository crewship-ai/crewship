package api

import (
	"net/http"
	"testing"
)

func TestProvisionCreateOnlyPreservesExistingAccountAndSetup(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unclaimed", true: "claimed"}[claimed], func(t *testing.T) {
			h, owner, ws := provisionRig(t)
			seedOtherUser(t, h, "existing-person", "person@example.test")
			if claimed {
				if _, err := h.db.Exec(`UPDATE users SET hashed_password='existing-password' WHERE id='existing-person'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.db.Exec(`INSERT INTO verification_tokens(identifier,token,expires,purpose) VALUES('person@example.test','untouched-token','2099-01-01T00:00:00Z','account_setup')`); err != nil {
				t.Fatal(err)
			}
			rr := provisionReq(t, h, owner, ws, "OWNER", `{"email":"person@example.test","role":"ADMIN","create_only":true}`)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status %d", rr.Code)
			}
			var members, tokens int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id='existing-person'`, ws).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier='person@example.test' AND token='untouched-token'`).Scan(&tokens); err != nil {
				t.Fatal(err)
			}
			if members != 0 || tokens != 1 {
				t.Fatalf("existing identity changed: memberships=%d tokens=%d", members, tokens)
			}
		})
	}
}

func TestProvisionCreateOnlyCreatesFreshAccount(t *testing.T) {
	h, owner, ws := provisionRig(t)
	rr := provisionReq(t, h, owner, ws, "OWNER", `{"email":"new-demo@example.test","role":"VIEWER","create_only":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d", rr.Code)
	}
	out := decodeProvision(t, rr)
	if !out.CreatedUser || out.Role != "VIEWER" || out.SetupURL == "" {
		t.Fatal("new account was not provisioned with its requested role and setup")
	}
}
