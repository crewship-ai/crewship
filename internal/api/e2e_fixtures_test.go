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
	"github.com/crewship-ai/crewship/internal/inbox"
)

// e2e_fixtures_test.go — the browser-test seed door (#2403).
//
// The Playwright PR gate needs a real run_needs_human card without a live
// agent run. The door that plants one exists ONLY on a router built with
// WithE2EFixtures, which the server passes only when CREWSHIP_E2E_FIXTURES
// is set. The first test pins the "only": a router built the ordinary way
// answers the path exactly as it answers a path that does not exist.

const e2eFixturePath = "/api/v1/e2e/fixtures/run-needs-human"

// newE2EFixtureRig seeds one owner, one workspace, one crew with one agent and
// one issue, and returns a router (with or without the gate) plus a bearer
// token for the owner.
func newE2EFixtureRig(t *testing.T, gated bool) (*Router, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-e2e", wsID, "Engineering", "engineering")
	if _, err := db.Exec(`UPDATE crews SET issue_prefix = 'ENG' WHERE id = 'crew-e2e'`); err != nil {
		t.Fatalf("issue prefix: %v", err)
	}
	seedAgentRow(t, db, "agent-e2e", wsID, "crew-e2e", "Casey", "casey", "AGENT")
	seedIssue(t, db, wsID, "crew-e2e", "agent-e2e", "ENG-7", "IN_PROGRESS")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	opts := []RouterOption{
		WithSocketPath("/tmp/crewship-e2e-fixture-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	}
	if gated {
		opts = append(opts, WithE2EFixtures())
	}
	r, err := NewRouter(db, secret, newTestLogger(), opts...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r.assignmentHandler != nil {
		t.Cleanup(r.assignmentHandler.WaitDispatches)
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
	return r, wsID, tok
}

func e2eFixturePost(r *Router, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestE2EFixtures_RouteAbsentWithoutTheGate(t *testing.T) {
	r, wsID, tok := newE2EFixtureRig(t, false)

	for _, mr := range r.mutationRoutes {
		if strings.HasPrefix(mr.Pattern, "/api/v1/e2e/") {
			t.Fatalf("an ungated router recorded the fixture route %s %s", mr.Method, mr.Pattern)
		}
	}

	got := e2eFixturePost(r, e2eFixturePath+"?workspace_id="+wsID, tok, `{"issue_identifier":"ENG-7"}`)
	missing := e2eFixturePost(r, "/api/v1/e2e/no-such-route?workspace_id="+wsID, tok, `{}`)
	if got.Code != http.StatusNotFound {
		t.Fatalf("ungated router answered %d for the fixture path, want 404; body=%s", got.Code, got.Body.String())
	}
	if got.Code != missing.Code || got.Body.String() != missing.Body.String() {
		t.Fatalf("the fixture path is distinguishable from a route that does not exist:\n fixture: %d %s\n missing: %d %s",
			got.Code, got.Body.String(), missing.Code, missing.Body.String())
	}
}

func TestE2EFixtures_FeedbackMessage(t *testing.T) {
	r, wsID, tok := newE2EFixtureRig(t, true)
	path := "/api/v1/e2e/fixtures/feedback-message?workspace_id=" + wsID
	rr := e2eFixturePost(r, path, tok, `{}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("fixture status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var fx struct {
		ChatID    string `json:"chat_id"`
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &fx); err != nil || fx.ChatID == "" || fx.MessageID == "" {
		t.Fatalf("fixture response = %+v, err=%v", fx, err)
	}
	feedback := e2eFixturePost(r, "/api/v1/feedback?workspace_id="+wsID, tok,
		`{"message_id":"`+fx.MessageID+`","signal":"helpful"}`)
	if feedback.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d; body=%s", feedback.Code, feedback.Body.String())
	}
	var ownerWS, ownerUser, role string
	if err := r.db.QueryRow(`SELECT workspace_id, created_by FROM chats WHERE id = ?`, fx.ChatID).Scan(&ownerWS, &ownerUser); err != nil {
		t.Fatalf("chat lookup: %v", err)
	}
	if err := r.db.QueryRow(`SELECT role FROM conversation_messages WHERE id = ? AND session_id = ?`, fx.MessageID, fx.ChatID).Scan(&role); err != nil {
		t.Fatalf("message lookup: %v", err)
	}
	if ownerWS != wsID || ownerUser == "" || role != "assistant" {
		t.Fatalf("fixture scope: workspace=%q user=%q role=%q", ownerWS, ownerUser, role)
	}

	ungated, _, ungatedTok := newE2EFixtureRig(t, false)
	absent := e2eFixturePost(ungated, path, ungatedTok, `{}`)
	if absent.Code != http.StatusNotFound {
		t.Fatalf("ungated fixture status = %d; body=%s", absent.Code, absent.Body.String())
	}
}

func TestE2EFixtures_EnabledFromEnv(t *testing.T) {
	cases := []struct {
		flag, env string
		want      bool
	}{
		{"", "", false},
		{"0", "", false},
		{"yes", "", false},
		{"1", "", true},
		{"true", "", true},
		{"1", "development", true},
		{"1", "prod", false},
		{"true", "Production", false},
	}
	for _, tc := range cases {
		getenv := func(k string) string {
			switch k {
			case "CREWSHIP_E2E_FIXTURES":
				return tc.flag
			case "CREWSHIP_ENV":
				return tc.env
			}
			return ""
		}
		if got := E2EFixturesEnabled(getenv); got != tc.want {
			t.Errorf("CREWSHIP_E2E_FIXTURES=%q CREWSHIP_ENV=%q: enabled = %v, want %v", tc.flag, tc.env, got, tc.want)
		}
	}
}

func TestE2EFixtures_RunNeedsHuman_PlantsACardTheActDoorResolves(t *testing.T) {
	r, wsID, tok := newE2EFixtureRig(t, true)

	rr := e2eFixturePost(r, e2eFixturePath+"?workspace_id="+wsID, tok, `{"issue_identifier":"ENG-7","reason":"staging or prod bucket?"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("fixture status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var fx struct {
		InboxItemID     string `json:"inbox_item_id"`
		AssignmentID    string `json:"assignment_id"`
		SessionID       string `json:"session_id"`
		MissionID       string `json:"mission_id"`
		IssueIdentifier string `json:"issue_identifier"`
		AgentID         string `json:"agent_id"`
		CrewID          string `json:"crew_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &fx); err != nil {
		t.Fatalf("decode fixture response: %v", err)
	}
	if fx.InboxItemID == "" || fx.AssignmentID == "" || fx.SessionID == "" || fx.IssueIdentifier != "ENG-7" || fx.AgentID != "agent-e2e" || fx.CrewID != "crew-e2e" {
		t.Fatalf("fixture response = %+v", fx)
	}

	// The card is the real B6 shape: bound to the run, threaded by issue,
	// carrying the three actions, and its session is parked awaiting input.
	var kind, sourceID, state, threadKey, actionsJSON, body string
	if err := r.db.QueryRow(`SELECT kind, source_id, state, COALESCE(thread_key,''), COALESCE(actions_json,'[]'), body_md FROM inbox_items WHERE id = ?`, fx.InboxItemID).
		Scan(&kind, &sourceID, &state, &threadKey, &actionsJSON, &body); err != nil {
		t.Fatalf("card row: %v", err)
	}
	if kind != inbox.KindRunNeedsHuman || sourceID != fx.AssignmentID || state != "unread" {
		t.Fatalf("card = kind %s source %s state %s", kind, sourceID, state)
	}
	if threadKey != "issue:"+wsID+":"+fx.MissionID {
		t.Fatalf("thread_key = %q", threadKey)
	}
	if !strings.Contains(actionsJSON, `"answer"`) || !strings.Contains(actionsJSON, `"take_over"`) || !strings.Contains(actionsJSON, `"dismiss"`) {
		t.Fatalf("actions = %s", actionsJSON)
	}
	if body != "staging or prod bucket?" {
		t.Fatalf("body = %q", body)
	}
	var sessState, outcome, status string
	if err := r.db.QueryRow(`SELECT s.state, a.outcome, a.status FROM assignments a JOIN issue_agent_sessions s ON s.id = a.session_id WHERE a.id = ?`, fx.AssignmentID).
		Scan(&sessState, &outcome, &status); err != nil {
		t.Fatalf("run row: %v", err)
	}
	if sessState != "awaiting_input" || outcome != "NEEDS_HUMAN" || status != "COMPLETED" {
		t.Fatalf("session %s, run outcome %s status %s", sessState, outcome, status)
	}

	// The same fixture twice on one issue refreshes the one card (B10's
	// subject threading) rather than doubling it.
	again := e2eFixturePost(r, e2eFixturePath+"?workspace_id="+wsID, tok, `{"issue_identifier":"ENG-7"}`)
	if again.Code != http.StatusCreated {
		t.Fatalf("second fixture status = %d; body=%s", again.Code, again.Body.String())
	}
	var cards int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND workspace_id = ?`, inbox.KindRunNeedsHuman, wsID).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("cards = %d (err %v), want 1", cards, err)
	}

	// Answering it through the real act door resolves the card and writes
	// the inbox_acted receipt on the issue's event log — the end-to-end
	// property the browser flow asserts.
	act := e2eFixturePost(r, "/api/v1/inbox/"+fx.InboxItemID+"/act?workspace_id="+wsID, tok, `{"action":"answer","input":"Use the staging bucket."}`)
	if act.Code != http.StatusOK {
		t.Fatalf("act status = %d, want 200; body=%s", act.Code, act.Body.String())
	}
	if r.assignmentHandler != nil {
		r.assignmentHandler.WaitDispatches()
	}
	var cardState string
	if err := r.db.QueryRow(`SELECT state FROM inbox_items WHERE id = ?`, fx.InboxItemID).Scan(&cardState); err != nil || cardState != "resolved" {
		t.Fatalf("card state = %q (err %v), want resolved", cardState, err)
	}
	var acted int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ? AND action = 'inbox_acted'`, fx.MissionID).Scan(&acted); err != nil || acted != 1 {
		t.Fatalf("inbox_acted rows = %d (err %v), want 1", acted, err)
	}
}

func TestE2EFixtures_RunNeedsHuman_UnknownIssueIs404(t *testing.T) {
	r, wsID, tok := newE2EFixtureRig(t, true)
	rr := e2eFixturePost(r, e2eFixturePath+"?workspace_id="+wsID, tok, `{"issue_identifier":"ENG-999"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	rr = e2eFixturePost(r, e2eFixturePath+"?workspace_id="+wsID, tok, `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
