package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// TestSteerRoute_SetSteererWiresThrough drives the production Router's
// POST /api/v1/chats/{chatId}/steer route end-to-end: an authenticated
// member of the chat's workspace reaches the SteerHandler, and once
// Router.SetSteerer flips the steerer live the request is delivered to it
// (returning 202). This exercises both the route registration and the
// two-phase SetSteerer wiring.
func TestSteerRoute_SetSteererWiresThrough(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-s", wsID, "C", "c-steer")
	seedAgentRow(t, db, "agent-s", wsID, "crew-s", "A", "a-steer", "AGENT")
	const chatID = "chat-steer-route"
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, 'agent-s', ?, ?, 's')`, chatID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-steer-route-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	fs := &fakeSteerer{res: chatbridge.SteerResult{Queued: true, InFlight: false}}
	r.SetSteerer(fs)

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(context.Background(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/"+chatID+"/steer?workspace_id="+wsID,
		strings.NewReader(`{"message":"focus on the auth bug"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rr.Code, rr.Body.String())
	}
	if fs.calls != 1 || fs.gotChat != chatID {
		t.Errorf("steerer not reached correctly: calls=%d chat=%q", fs.calls, fs.gotChat)
	}
}
