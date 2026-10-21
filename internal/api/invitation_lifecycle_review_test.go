package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInvitationReviewTargetRoleConflictAndCAS(t *testing.T) {
	for _, scenario := range []string{"different-role", "same-role", "lost-cas"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupTestDB(t)
			inviter, ws := seedInviter(t, db)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('review-target','target-review@example.test')`)
			seedInvitation(t, db, "review-target", ws, inviter, "target-review@example.test", "MANAGER", time.Now().Add(time.Hour), nil)
			if scenario != "lost-cas" {
				role := "VIEWER"
				if scenario == "same-role" {
					role = "MANAGER"
				}
				execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('review-target-member',?,'review-target',?)`, ws, role)
			}
			if scenario == "lost-cas" {
				execOrFatal(t, db, `CREATE TRIGGER lose_invitation_cas BEFORE UPDATE OF accepted_at ON workspace_invitations BEGIN SELECT RAISE(IGNORE); END`)
			}
			router, err := NewRouter(db, "review-target-role-secret-long-enough", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			token := mintTokenFor(t, db, "review-target", "reviewtarget"+strings.Repeat("0", 40))
			req := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(`{"token":"tok-review-target"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			want := 409
			if scenario == "same-role" {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("%s status=%d want%d body=%s", scenario, rr.Code, want, rr.Body.String())
			}
			var count, accepted int
			var role string
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id='review-target'`, ws).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='review-target' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil {
				t.Fatal(err)
			}
			if scenario == "lost-cas" {
				if count != 0 || accepted != 0 {
					t.Fatalf("lost CAS effects count%d accepted%d", count, accepted)
				}
				return
			}
			if err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id='review-target'`, ws).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if scenario == "different-role" && (role != "VIEWER" || accepted != 0) {
				t.Fatalf("conflict changed state role%s accepted%d", role, accepted)
			}
			if scenario == "same-role" && (role != "MANAGER" || accepted != 1) {
				t.Fatalf("same role receipt state role%s accepted%d", role, accepted)
			}
		})
	}
}

func TestInvitationReviewNewProofIsHashedAtRest(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	h := NewWorkspaceHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"hash-review@example.test","role":"MEMBER"}`)), inviter, ws, "OWNER"))
	if rr.Code != 201 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var inv invitationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token FROM workspace_invitations WHERE id=?`, inv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == inv.Token || !strings.HasPrefix(stored, "sha256:") {
		t.Fatal("new invitation stores raw proof instead of tagged hash")
	}
}

func TestInvitationReviewReissueAndRevokeHTTP(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	seedInvitation(t, db, "review-rotate", ws, inviter, "rotate-review@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	router, err := NewRouter(db, "review-reissue-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	owner := mintTokenFor(t, db, inviter, "reviewissuer"+strings.Repeat("0", 40))
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	base := "/api/v1/workspaces/" + ws + "/invitations/review-rotate"
	rr := request("POST", base+"/reissue")
	if rr.Code != 200 {
		t.Fatalf("reissue=%d body=%s", rr.Code, rr.Body.String())
	}
	var inv invitationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &inv); err != nil || inv.Token == "" || inv.Token == "tok-review-rotate" {
		t.Fatal("missing fresh issuer proof", err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token FROM workspace_invitations WHERE id='review-rotate'`).Scan(&stored); err != nil || stored == "tok-review-rotate" || stored == inv.Token {
		t.Fatal("rotation failed to retire old proof or hashed storage", err)
	}
	rr = request("DELETE", base)
	if rr.Code != 200 {
		t.Fatalf("revoke=%d body=%s", rr.Code, rr.Body.String())
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='review-rotate' AND accepted_at IS NULL AND julianday(expires_at)>julianday('now')`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("revoked proof still live", pending, err)
	}
}

func TestInvitationReviewStoredDigestIsNotBearerProof(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('digest-target','digest-review@example.test')`)
	handler := NewWorkspaceHandler(db, newTestLogger())
	created := httptest.NewRecorder()
	handler.CreateInvitation(created, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"digest-review@example.test","role":"MEMBER"}`)), inviter, ws, "OWNER"))
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var inv invitationResponse
	if err := json.Unmarshal(created.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token FROM workspace_invitations WHERE id=?`, inv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, "digest-proof-test-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	session := mintTokenFor(t, db, "digest-target", "digesttarget"+strings.Repeat("0", 40))
	request := func(proof string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"token": proof})
		req := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+session)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	for _, proof := range []string{stored, strings.TrimPrefix(stored, "sha256:v1:"), "sha256:v2:" + strings.Repeat("0", 64), "wi2_" + strings.Repeat("0", 64), "wi1_" + strings.Repeat("0", 64)} {
		rr := request(proof)
		if rr.Code != 400 && rr.Code != 404 {
			t.Fatalf("non-bearer format accepted status%d", rr.Code)
		}
		var effects int
		if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM workspace_members WHERE user_id='digest-target')+(SELECT COUNT(*) FROM workspace_invitations WHERE id=? AND accepted_at IS NOT NULL)`, inv.ID).Scan(&effects); err != nil || effects != 0 {
			t.Fatal("denied proof changed DB", effects, err)
		}
	}
	if rr := request(inv.Token); rr.Code != 200 {
		t.Fatal("actual raw proof rejected", rr.Code, rr.Body.String())
	}
}

func TestInvitationReviewConcurrentAcceptanceClaimsOnce(t *testing.T) {
	db := concurrentTestDB(t)
	inviter, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('race-target','race-review@example.test')`)
	seedInvitation(t, db, "race-target", ws, inviter, "race-review@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	execOrFatal(t, db, `CREATE TABLE invitation_claim_audit(id TEXT)`)
	execOrFatal(t, db, `CREATE TRIGGER audit_invitation_claim AFTER UPDATE OF accepted_at ON workspace_invitations WHEN NEW.accepted_at IS NOT NULL BEGIN INSERT INTO invitation_claim_audit(id) VALUES(NEW.id);END`)
	router, err := NewRouter(db, "race-proof-test-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	session := mintTokenFor(t, db, "race-target", "racetarget"+strings.Repeat("0", 40))
	start := make(chan struct{})
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			req := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(`{"token":"tok-race-target"}`))
			req.Header.Set("Authorization", "Bearer "+session)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			results <- rr.Code
		}()
	}
	close(start)
	for i := 0; i < 8; i++ {
		if status := <-results; status != 200 {
			t.Fatalf("concurrent acceptance status%d", status)
		}
	}
	var members, claims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id='race-target'`, ws).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM invitation_claim_audit WHERE id='race-target'`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if members != 1 || claims != 1 {
		t.Fatalf("concurrent effects members%d claims%d", members, claims)
	}
}

func TestInvitationReviewCreationRechecksLiveIssuer(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	execOrFatal(t, db, `UPDATE workspace_members SET role='VIEWER' WHERE user_id=?`, inviter)
	h := NewWorkspaceHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"stale-context@example.test","role":"MEMBER"}`)), inviter, ws, "OWNER"))
	if rr.Code != 403 {
		t.Fatalf("stale issuer context admitted creation status%d", rr.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE email='stale-context@example.test'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("unauthorized invitation row", n, err)
	}
}
