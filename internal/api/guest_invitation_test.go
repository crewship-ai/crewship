package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestGuestInvitationAtomicallyInstallsInitialGrants(t *testing.T) {
	for _, scenario := range []string{"accept", "resource-removed", "provision", "provision-invalid"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupTestDB(t)
			owner, ws := seedInviter(t, db)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('guest-user','guest@example.test')`)
			execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('guest-crew',?,'Crew','guest-crew')`, ws)
			seedAgentRow(t, db, "guest-agent", ws, "guest-crew", "Shared", "shared", "AGENT")
			router, err := NewRouter(db, "guest-invitation-test-secret-long-enough", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			tokens := map[string]string{owner: mintTokenFor(t, db, owner, "guest-owner-"+strings.Repeat("0", 40)), "guest-user": mintTokenFor(t, db, "guest-user", "guest-user-"+strings.Repeat("0", 40))}
			request := func(user, path string, body any, want int) *httptest.ResponseRecorder {
				t.Helper()
				raw, _ := json.Marshal(body)
				req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
				req.Header.Set("Authorization", "Bearer "+tokens[user])
				req.Header.Set("X-Workspace-ID", ws)
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != want {
					t.Fatalf("%s = %d want %d: %s", path, rr.Code, want, rr.Body.String())
				}
				return rr
			}
			rights := []access.Right{{Kind: "agent", ID: "guest-agent", Operation: "discover"}, {Kind: "agent", ID: "guest-agent", Operation: "chat"}}
			body := map[string]any{"email": "guest@example.test", "role": "VIEWER", "access_mode": "restricted", "rights": rights}
			if strings.HasPrefix(scenario, "provision") {
				body["email"] = "new-guest@example.test"
				want := 201
				if scenario == "provision-invalid" {
					body["rights"] = []access.Right{{Kind: "agent", ID: "missing", Operation: "chat"}}
					want = 404
				}
				request(owner, "/api/v1/workspaces/"+ws+"/members/provision", body, want)
				if scenario == "provision-invalid" {
					var n int
					if err = db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='new-guest@example.test'`).Scan(&n); err != nil || n != 0 {
						t.Fatalf("failed guest provisioning left an account: %d %v", n, err)
					}
					return
				}
				var user string
				if err = db.QueryRow(`SELECT id FROM users WHERE email='new-guest@example.test'`).Scan(&user); err != nil {
					t.Fatal(err)
				}
				policy, err := (access.Store{DB: db}).Policy(t.Context(), owner, user, ws)
				if err != nil || policy.Mode != "restricted" || len(policy.Rights) != 2 {
					t.Fatalf("guest policy=%+v %v", policy, err)
				}
				return
			}
			created := request(owner, "/api/v1/workspaces/"+ws+"/invitations", body, 201)
			var inv invitationResponse
			if err = json.Unmarshal(created.Body.Bytes(), &inv); err != nil {
				t.Fatal(err)
			}
			confirmation := request("guest-user", "/api/v1/auth/invitation/accept", map[string]string{"token": inv.Token}, 400)
			if !strings.Contains(confirmation.Body.String(), "guest_confirmation_required") {
				t.Fatal("Guest account restriction was not disclosed")
			}
			var prior int
			if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='guest-user'`).Scan(&prior); err != nil || prior != 0 {
				t.Fatal("Guest mode applied before confirmation", prior, err)
			}
			if scenario == "resource-removed" {
				execOrFatal(t, db, `DELETE FROM agents WHERE id='guest-agent'`)
			}
			want := 200
			if scenario == "resource-removed" {
				want = 409
			}
			request("guest-user", "/api/v1/auth/invitation/accept", map[string]any{"token": inv.Token, "confirm_account_restriction": true}, want)
			if scenario == "resource-removed" {
				var n int
				if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='guest-user'`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("failed redemption left membership: %d %v", n, err)
				}
				return
			}
			policy, err := (access.Store{DB: db}).Policy(t.Context(), owner, "guest-user", ws)
			if err != nil || policy.Mode != "restricted" || len(policy.Rights) != 2 {
				t.Fatalf("guest policy=%+v %v", policy, err)
			}
			// The same authenticated identity may replay acceptance after becoming
			// restricted, without restoring removed grants or creating membership.
			request("guest-user", "/api/v1/auth/invitation/accept", map[string]any{"token": inv.Token, "confirm_account_restriction": true}, 200)
		})
	}
}

func TestGuestSignupRedeemsRestrictedPolicy(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('signup-guest-crew',?,'Crew','signup-guest-crew')`, ws)
	seedAgentRow(t, db, "signup-guest-agent", ws, "signup-guest-crew", "Shared", "signup-shared", "AGENT")
	seedInvitation(t, db, "guest-signup", ws, owner, "signup-guest@example.test", "VIEWER", time.Now().Add(time.Hour), nil)
	execOrFatal(t, db, `UPDATE workspace_invitations SET access_mode='restricted',rights_json='[{"kind":"agent","id":"signup-guest-agent","operation":"discover"}]' WHERE id='guest-signup'`)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	rr := signupWithInvitationProof(t, h, "signup-guest@example.test", "tok-guest-signup")
	if rr.Code != 202 {
		t.Fatalf("signup=%d: %s", rr.Code, rr.Body.String())
	}
	var user string
	if err := db.QueryRow(`SELECT id FROM users WHERE email='signup-guest@example.test'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	policy, err := (access.Store{DB: db}).Policy(t.Context(), owner, user, ws)
	if err != nil || policy.Mode != "restricted" || len(policy.Rights) != 1 || policy.Rights[0].ID != "signup-guest-agent" {
		t.Fatalf("signup policy=%+v %v", policy, err)
	}
}

func TestGuestProvisionCannotRestrictAnExistingAccountWithoutConsent(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email,hashed_password) VALUES('claimed-guest','claimed-guest@example.test','synthetic-existing-password-hash')`)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('claimed-own-ws','Own','claimed-own-ws')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('claimed-own-membership','claimed-own-ws','claimed-guest','OWNER')`)
	router, err := NewRouter(db, "guest-provision-consent-test-secret-long", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, owner, "guest-consent-owner-"+strings.Repeat("0", 40))
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+ws+"/members/provision", strings.NewReader(`{"email":"claimed-guest@example.test","role":"VIEWER","access_mode":"restricted","rights":[]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", ws)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("unconsented restriction=%d: %s", rr.Code, rr.Body.String())
	}
	restricted, err := (access.Store{DB: db}).HasRestrictedMembership(t.Context(), "claimed-guest")
	if err != nil || restricted {
		t.Fatalf("existing account was restricted: %t %v", restricted, err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='claimed-guest'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed provisioning changed memberships: %d %v", n, err)
	}
}

func TestGuestInvitationAndProvisionPreserveOtherWorkspaceAdministrators(t *testing.T) {
	for _, role := range []string{"OWNER", "ADMIN"} {
		for _, route := range []string{"accept", "provision"} {
			t.Run(role+"/"+route, func(t *testing.T) {
				db := setupTestDB(t)
				owner, ws := seedInviter(t, db)
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('protected-admin','protected@example.test')`)
				execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('protected-workspace','Protected','protected-workspace')`)
				execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('protected-member','protected-workspace','protected-admin',?)`, role)
				seedInvitation(t, db, "protected-invite", ws, owner, "protected@example.test", "VIEWER", time.Now().Add(time.Hour), nil)
				execOrFatal(t, db, `UPDATE workspace_invitations SET access_mode='restricted' WHERE id='protected-invite'`)
				router, err := NewRouter(db, "protected-admin-test-secret-long-enough", newTestLogger())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(router.Shutdown)
				actor, path, body := "protected-admin", "/api/v1/auth/invitation/accept", `{"token":"tok-protected-invite","confirm_account_restriction":true}`
				if route == "provision" {
					actor = owner
					path = "/api/v1/workspaces/" + ws + "/members/provision"
					body = `{"email":"protected@example.test","role":"VIEWER","access_mode":"restricted","rights":[]}`
				}
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Workspace-ID", ws)
				req.Header.Set("Authorization", "Bearer "+mintTokenFor(t, db, actor, "protected-"+strings.Repeat("0", 40)))
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 409 || !strings.Contains(rr.Body.String(), "guest_administrator_conflict") {
					t.Fatalf("protected admin=%d %s", rr.Code, rr.Body.String())
				}
				var count int
				if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='protected-admin' AND access_mode='trusted' AND role=?`, role).Scan(&count); err != nil || count != 1 {
					t.Fatal("administrator changed", count, err)
				}
				if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='protected-admin'`).Scan(&count); err != nil || count != 1 {
					t.Fatal("partial membership", count, err)
				}
			})
		}
	}
}

func TestGuestSignupStaleGrantReturnsConflictWithoutAccount(t *testing.T) {
	for _, change := range []string{"resource-removed", "issuer-demoted"} {
		t.Run(change, func(t *testing.T) {
			db := setupTestDB(t)
			owner, ws := seedInviter(t, db)
			seedInvitation(t, db, "stale-guest", ws, owner, "stale@example.test", "VIEWER", time.Now().Add(time.Hour), nil)
			execOrFatal(t, db, `UPDATE workspace_invitations SET access_mode='restricted',rights_json='[{"kind":"agent","id":"missing-agent","operation":"chat"}]' WHERE id='stale-guest'`)
			if change == "issuer-demoted" {
				execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, owner)
			}
			h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
			rr := httptest.NewRecorder()
			h.Signup(rr, httptest.NewRequest("POST", "/signup", strings.NewReader(`{"email":"stale@example.test","password":"test-password-long","full_name":"Stale","invitation_token":"tok-stale-guest"}`)))
			if rr.Code != 409 || !strings.Contains(rr.Body.String(), "invitation_changed") {
				t.Fatalf("stale Guest signup=%d %s", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='stale@example.test'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial account", count, err)
			}
		})
	}
}
