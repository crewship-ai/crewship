package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatshare"
	"github.com/crewship-ai/crewship/internal/ws"
)

func chatShareBoundaryFixture(t *testing.T, socketPath string) (*ChatSharesHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status)
		VALUES ('share-agent', ?, 'Share Agent', 'share-agent', 'IDLE')`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, status)
		VALUES ('share-chat', 'share-agent', ?, ?, 'ACTIVE')`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	grant, token, err := chatshare.NewStore(db).Create(t.Context(), wsID, "share-agent", "share-chat", userID, time.Hour)
	if err != nil {
		t.Fatalf("create valid share: %v", err)
	}
	return NewChatSharesHandler(db, NewProxyHandler(db, newTestLogger(), socketPath), newTestLogger()), db, grant.ID, token
}

func readSharedChatBoundary(h *ChatSharesHandler, shareID, token, query, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/shared-chats/"+shareID+"/messages"+query, nil)
	r.SetPathValue("shareId", shareID)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: cookie})
	}
	w := httptest.NewRecorder()
	h.Messages(w, r)
	return w
}

func TestChatShareBoundary_OnlyBearerAndRevocationBeforeIPC(t *testing.T) {
	var upstream atomic.Int32
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if r.URL.Path != "/chats/share-chat/shared-messages" {
			t.Errorf("IPC path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	h, db, id, token := chatShareBoundaryFixture(t, sock)

	if rr := readSharedChatBoundary(h, id, token, "", ""); rr.Code != http.StatusOK {
		t.Fatalf("valid bearer = %d: %s", rr.Code, rr.Body.String())
	}
	for _, tc := range []struct{ name, bearer, query, cookie string }{
		{"query token", "", "?token=" + token, ""},
		{"query with bearer", token, "?limit=1", ""},
		{"cookie token", "", "", token},
		{"wrong bearer", "cshr_" + strings.Repeat("A", 43), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := upstream.Load()
			rr := readSharedChatBoundary(h, id, tc.bearer, tc.query, tc.cookie)
			if rr.Code >= 200 && rr.Code < 300 {
				t.Errorf("unexpected success: %d %s", rr.Code, rr.Body.String())
			}
			if upstream.Load() != before {
				t.Error("unauthorized request reached IPC")
			}
		})
	}
	if err := chatshare.NewStore(db).Revoke(t.Context(), "test-workspace-id", "share-agent", "share-chat", id, "test-user-id"); err != nil {
		t.Fatal(err)
	}
	before := upstream.Load()
	rr := readSharedChatBoundary(h, id, token, "", "")
	if rr.Code == http.StatusOK || upstream.Load() != before {
		t.Fatalf("revoked share status=%d IPC calls=%d before=%d", rr.Code, upstream.Load(), before)
	}
}

func TestChatShareBoundary_ProjectsOnlyConversationText(t *testing.T) {
	const secret = "SENSITIVE_CANARY"
	fixture := fmt.Sprintf(`{"messages":[
		{"id":"u","role":"user","content":"public user","ts":"2026-01-01T00:00:00Z","metadata":{"secret":"%s"},"author_user_id":"%s"},
		{"id":"a","role":"assistant","content":"flattened %s","parts":[{"type":"thinking","content":"%s"},{"type":"text","content":"public assistant"},{"type":"tool_call","content":"%s","tool_name":"secret_tool"},{"type":"tool_result","content":"%s"},{"type":"image","content":"%s"}],"tool_name":"%s","tool_summary":"%s","metadata":{"reasoning":"%s"},"ts":"2026-01-01T00:00:01Z"},
		{"id":"sys","role":"system","content":"%s","ts":"2026-01-01T00:00:02Z"},
		{"id":"tool","role":"tool","content":"%s","ts":"2026-01-01T00:00:03Z"}
	]}`, secret, secret, secret, secret, secret, secret, secret, secret, secret, secret, secret, secret)
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	h, _, id, token := chatShareBoundaryFixture(t, sock)
	rr := readSharedChatBoundary(h, id, token, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) || strings.Contains(rr.Body.String(), "parts") || strings.Contains(rr.Body.String(), "tool_") || strings.Contains(rr.Body.String(), "metadata") || strings.Contains(rr.Body.String(), "author_user_id") {
		t.Fatalf("non-text transcript data leaked: %s", rr.Body.String())
	}
	var body struct {
		Messages []struct {
			ID, Role, Content string
		} `json:"messages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Content != "public user" || body.Messages[1].Content != "public assistant" {
		t.Fatalf("unexpected projection: %+v", body.Messages)
	}
}

func TestChatShareBoundary_CannotAuthenticateNormalOrStreamRoutes(t *testing.T) {
	_, db, _, token := chatShareBoundaryFixture(t, "/tmp/crewship-share-boundary-no-ipc.sock")
	router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger(),
		WithSocketPath("/tmp/crewship-share-boundary-router.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/workspaces",
		"/api/v1/ws-token",
		"/api/v1/chats/share-chat/messages?workspace_id=test-workspace-id",
		"/api/v1/chats/share-chat/stream",
		"/api/v1/agents/share-agent/chats?workspace_id=test-workspace-id",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("share bearer authenticated %s: status=%d body=%s", path, rr.Code, rr.Body.String())
			}
		})
	}
	channelAuth := ws.NewDBChannelAuthorizer(db)
	for _, channel := range []string{"session:share-chat", "agent:share-agent"} {
		allowed, err := channelAuth.CanSubscribe(t.Context(), token, channel)
		if err != nil || allowed {
			t.Errorf("share token subscribed to %s: allowed=%v err=%v", channel, allowed, err)
		}
	}
}
