package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// TestFeedbackRoute_ThroughProductionRouter drives POST/GET/DELETE
// /api/v1/feedback end-to-end through the production Router (NewRouter +
// ServeHTTP), rather than calling MessageFeedbackHandler directly the way
// message_feedback_test.go does. #1617's second half reported the
// registered POST route 404ing and pointed at authedSelfMut (used for
// POST/DELETE here) differing from the plain r.mux.Handle used for GET on
// the adjacent line as the likely cause — Go 1.22+ ServeMux pattern
// shadowing, or the wrapper registering a different method/pattern shape
// than what it logs.
//
// This test pins that authedSelfMut and the plain r.mux.Handle produce the
// same routable outcome for all three verbs on the same path — a request
// with a real, visible message reaches the handler and gets a real
// response, not a router-level 404 ("404 page not found", net/http's
// default NotFoundHandler body). A regression in the wrapper's pattern
// string (e.g. a stray space, wrong method casing, or a pattern that
// collides with a broader registration) would make this fail with that
// exact body instead of the handler's JSON response.
func TestFeedbackRoute_ThroughProductionRouter(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-fbr", wsID, "C", "c-fbr")
	seedAgentRow(t, db, "agent-fbr", wsID, "crew-fbr", "A", "a-fbr", "AGENT")

	const chatID = "chat-fbr-route"
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, 'agent-fbr', ?, ?, 'fbr')`, chatID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	const messageID = "msg-fbr-route-1"
	seedConvMessage(t, db, messageID, chatID, "agent-fbr")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-feedback-route-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

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
	authed := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+tok)
		return req
	}

	// A real router-level 404 (no matching pattern) always answers this
	// exact body — net/http's default NotFoundHandler. The feedback
	// handler's own 404s are JSON ({"error":"..."}) and are therefore
	// distinguishable from a routing miss by body shape alone.
	const routingMissBody = "404 page not found\n"

	t.Run("POST reaches the handler, not a router 404", func(t *testing.T) {
		body := `{"message_id":"` + messageID + `","signal":"helpful","reason":"route test"}`
		req := authed(httptest.NewRequest(http.MethodPost, "/api/v1/feedback?workspace_id="+wsID, strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Body.String() == routingMissBody {
			t.Fatalf("POST /api/v1/feedback hit the router's default NotFoundHandler — the route did not match at all")
		}
		if rr.Code != http.StatusCreated {
			t.Fatalf("status: got %d want %d; body=%s", rr.Code, http.StatusCreated, rr.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
		}
		if created.ID == "" {
			t.Errorf("expected a persisted feedback id, got empty")
		}
	})

	t.Run("GET reaches the handler and lists the row", func(t *testing.T) {
		req := authed(httptest.NewRequest(http.MethodGet, "/api/v1/feedback?message_id="+messageID+"&workspace_id="+wsID, nil))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Body.String() == routingMissBody {
			t.Fatalf("GET /api/v1/feedback hit the router's default NotFoundHandler")
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var listed struct {
			Feedback []struct {
				Signal string `json:"signal"`
			} `json:"feedback"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
		}
		if len(listed.Feedback) != 1 || listed.Feedback[0].Signal != "helpful" {
			t.Errorf("expected one 'helpful' row from the POST above, got %+v", listed.Feedback)
		}
	})

	t.Run("DELETE reaches the handler, not a router 404", func(t *testing.T) {
		req := authed(httptest.NewRequest(http.MethodDelete, "/api/v1/feedback?message_id="+messageID+"&signal=helpful&workspace_id="+wsID, nil))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Body.String() == routingMissBody {
			t.Fatalf("DELETE /api/v1/feedback hit the router's default NotFoundHandler")
		}
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status: got %d want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
		}
	})
}
