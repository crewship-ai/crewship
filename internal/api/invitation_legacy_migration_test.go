package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// This is migration acceptance coverage, not a reproduction of a main vulnerability.
func TestInvitationLegacyInvalidationAndReplacement(t *testing.T) {
	for _, operation := range []string{"new-invitation", "reissue"} {
		t.Run(operation, func(t *testing.T) {
			db := setupTestDB(t)
			issuer, ws := seedInviter(t, db)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('migration-target','migration@example.test')`)
			seedInvitation(t, db, "legacy-pending", ws, issuer, "migration@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
			// The shared seeder creates new hashed proofs. Explicitly restore the
			// historical raw representation for this upgrade fixture.
			execOrFatal(t, db, `UPDATE workspace_invitations SET token='tok-legacy-pending' WHERE id='legacy-pending'`)
			seedInvitation(t, db, "new-hash-pending", ws, issuer, "newhash@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
			var newHashBefore string
			if err := db.QueryRow(`SELECT token||'|'||expires_at FROM workspace_invitations WHERE id='new-hash-pending'`).Scan(&newHashBefore); err != nil {
				t.Fatal(err)
			}
			accepted := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			seedInvitation(t, db, "legacy-history", ws, issuer, "history@example.test", "VIEWER", time.Now().Add(time.Hour), &accepted)
			execOrFatal(t, db, `UPDATE workspace_invitations SET token='tok-legacy-history' WHERE id='legacy-history'`)
			var historyBefore string
			if err := db.QueryRow(`SELECT token||'|'||expires_at||'|'||accepted_at FROM workspace_invitations WHERE id='legacy-history'`).Scan(&historyBefore); err != nil {
				t.Fatal(err)
			}
			migration, err := os.ReadFile("../database/migrations/20261013164106_invalidate_legacy_pending_invitations.sql")
			if err != nil {
				t.Fatalf("required legacy invalidation migration: %v", err)
			}
			execOrFatal(t, db, string(migration))
			var newHashAfter string
			if err := db.QueryRow(`SELECT token||'|'||expires_at FROM workspace_invitations WHERE id='new-hash-pending'`).Scan(&newHashAfter); err != nil {
				t.Fatal(err)
			}
			if newHashAfter != newHashBefore {
				t.Fatal("new hashed pending proof changed")
			}
			var historyAfter string
			if err := db.QueryRow(`SELECT token||'|'||expires_at||'|'||accepted_at FROM workspace_invitations WHERE id='legacy-history'`).Scan(&historyAfter); err != nil {
				t.Fatal(err)
			}
			if historyAfter != historyBefore {
				t.Fatal("accepted historical invitation changed")
			}
			router, err := NewRouter(db, "migration-invitation-secret-long-enough", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			owner := mintTokenFor(t, db, issuer, "migrationowner"+strings.Repeat("0", 40))
			target := mintTokenFor(t, db, "migration-target", "migrationtarget"+strings.Repeat("0", 40))
			request := func(path, body, token string) *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			accept := func(proof string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(map[string]string{"token": proof})
				return request("/api/v1/auth/invitation/accept", string(body), target)
			}
			if rr := accept("tok-legacy-pending"); rr.Code != 404 {
				t.Fatalf("old proof status%d want404", rr.Code)
			}
			if rr := accept("revoked:legacy:legacy-pending"); rr.Code != 404 {
				t.Fatalf("migration marker accepted: %d", rr.Code)
			}
			path := "/api/v1/workspaces/" + ws + "/invitations"
			body := `{"email":"migration@example.test","role":"MEMBER"}`
			want := 201
			if operation == "reissue" {
				path += "/legacy-pending/reissue"
				body = ""
				want = 200
			}
			rr := request(path, body, owner)
			if rr.Code != want {
				t.Fatalf("replacement status%d want%d: %s", rr.Code, want, rr.Body.String())
			}
			var replacement invitationResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &replacement); err != nil || replacement.Token == "" {
				t.Fatal("missing replacement proof", err)
			}
			if rr := accept(replacement.Token); rr.Code != 200 {
				t.Fatalf("replacement proof acceptance%d: %s", rr.Code, rr.Body.String())
			}
			if rr := accept("tok-legacy-pending"); rr.Code != 404 {
				t.Fatalf("old proof revived: %d", rr.Code)
			}
			var count int
			var role string
			if err := db.QueryRow(`SELECT COUNT(*),MAX(role) FROM workspace_members WHERE workspace_id=? AND user_id='migration-target'`, ws).Scan(&count, &role); err != nil {
				t.Fatal(err)
			}
			if count != 1 || role != "MEMBER" {
				t.Fatalf("membership count%d role%s", count, role)
			}
		})
	}
}
