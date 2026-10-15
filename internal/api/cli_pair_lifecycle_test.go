package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestPairRedeem_SourceLifecycle(t *testing.T) {
	for _, state := range []string{"active", "suspended", "logout", "revoked", "deleted", "expired", "malformed", "wrong-owner"} {
		t.Run(state, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			store := sessions.NewDBStore(db)
			sess, err := store.Create(context.Background(), userID, "", "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			h := newPairHandler(t, db)
			req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", strings.NewReader(`{}`))
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, SessionID: sess.ID}))
			start := httptest.NewRecorder()
			h.Start(start, req)
			if start.Code != http.StatusOK {
				t.Fatalf("start: %d", start.Code)
			}
			var code pairStartResponse
			if err := json.Unmarshal(start.Body.Bytes(), &code); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "suspended":
				execOrFatal(t, db, `UPDATE users SET suspended_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), userID)
			case "logout", "revoked":
				if err := store.Revoke(context.Background(), sess.ID, state); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				execOrFatal(t, db, `DELETE FROM user_sessions WHERE id=?`, sess.ID)
			case "expired":
				execOrFatal(t, db, `UPDATE user_sessions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), sess.ID)
			case "malformed":
				execOrFatal(t, db, `UPDATE user_sessions SET expires_at='invalid' WHERE id=?`, sess.ID)
			case "wrong-owner":
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('other-pair-user','other-pair@example.com')`)
				execOrFatal(t, db, `UPDATE user_sessions SET user_id='other-pair-user' WHERE id=?`, sess.ID)
			}
			rr := covCPRedeem(h, code.Code)
			want := http.StatusBadRequest
			tokens := 0
			status := "pending"
			if state == "active" {
				want = http.StatusOK
				tokens = 1
				status = "consumed"
			}
			if rr.Code != want {
				t.Errorf("redeem status=%d, want %d", rr.Code, want)
			}
			var gotTokens int
			if err := db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id=?`, userID).Scan(&gotTokens); err != nil {
				t.Fatal(err)
			}
			if gotTokens != tokens {
				t.Errorf("minted %d tokens, want %d", gotTokens, tokens)
			}
			var gotStatus string
			if err := db.QueryRow(`SELECT status FROM cli_pairings WHERE code=?`, code.Code).Scan(&gotStatus); err != nil {
				t.Fatal(err)
			}
			if gotStatus != status {
				t.Errorf("pair status=%s, want %s", gotStatus, status)
			}
		})
	}
}

func pairTestSession(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	sess, err := sessions.NewDBStore(db).Create(context.Background(), userID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func TestPairStart_RequiresSourceSession(t *testing.T) {
	db := setupTestDB(t)
	uid := seedTestUser(t, db)
	h := newPairHandler(t, db)
	req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", strings.NewReader(`{}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: uid}))
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", rr.Code)
	}
}

func TestPairRedeem_RejectsLegacySource(t *testing.T) {
	h, uid := covCPFixture(t)
	covCPSeedPairing(t, h, uid, "AAAA-BBBB")
	execOrFatal(t, h.db, `UPDATE cli_pairings SET source_session_id=NULL`)
	rr := covCPRedeem(h, "AAAA-BBBB")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
	var tokens int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Errorf("minted %d tokens", tokens)
	}
}
