package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func chatShareManagementTestRequest(t *testing.T, h *ChatSharesHandler, method, path, body, userID, wsID string, scopes ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("agentId", "share-agent")
	r.SetPathValue("chatId", "share-chat")
	if strings.Contains(path, "/shares/") {
		r.SetPathValue("shareId", path[strings.LastIndex(path, "/")+1:])
	}
	if userID != "" {
		r = withWorkspaceUser(r, userID, wsID, "OWNER")
	}
	if scopes != nil {
		set := stringSet{}
		for _, scope := range scopes {
			set[scope] = struct{}{}
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxTokenScopes, set))
	}
	w := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		h.Create(w, r)
	case http.MethodGet:
		h.List(w, r)
	case http.MethodDelete:
		h.Revoke(w, r)
	}
	return w
}

func TestChatShareManagementHTTP_LifecycleAndValidation(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id,workspace_id,name,slug,status)
		VALUES ('share-agent',?,'Share Agent','share-agent','IDLE')`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats (id,agent_id,workspace_id,created_by,status,mode)
		VALUES ('share-chat','share-agent',?,?,'ACTIVE','CHAT')`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	h := NewChatSharesHandler(db, nil, newTestLogger())
	path := "/api/v1/agents/share-agent/chats/share-chat/shares"
	for _, tc := range []struct{ name, body string }{
		{"malformed", `{`},
		{"unknown field", `{"ttl_seconds":60,"chat_id":"other"}`},
		{"negative ttl", `{"ttl_seconds":-1}`},
		{"too long", `{"ttl_seconds":604801}`},
		{"second document", `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := chatShareManagementTestRequest(t, h, http.MethodPost, path, tc.body, userID, wsID)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodPost, path, `{}`, "", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("missing human context status=%d", rr.Code)
	}
	if _, err := db.Exec(`INSERT INTO users (id,email) VALUES ('share-other','share-other@example.test');
		INSERT INTO workspace_members (id,workspace_id,user_id,role)
		VALUES ('share-other-member',?,'share-other','MEMBER')`, wsID); err != nil {
		t.Fatal(err)
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodPost, path, `{}`, "share-other", wsID); rr.Code != http.StatusNotFound {
		t.Fatalf("same-workspace noncreator create status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodGet, path, "", "share-other", wsID); rr.Code != http.StatusNotFound {
		t.Fatalf("same-workspace noncreator list status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodPost, path, `{}`, userID, wsID, "agents:read"); rr.Code != http.StatusForbidden {
		t.Fatalf("read-scoped CLI create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := chatShareManagementTestRequest(t, h, http.MethodPost, path, `{}`, userID, wsID, "agents:write")
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d cache=%q body=%s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	var response struct {
		Share struct {
			ID        string    `json:"id"`
			ChatID    string    `json:"chat_id"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"share"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Share.ID == "" || response.Share.ChatID != "share-chat" || !strings.HasPrefix(response.Token, "cshr_") {
		t.Fatalf("invalid create response: %s", created.Body.String())
	}
	if ttl := time.Until(response.Share.ExpiresAt); ttl < 23*time.Hour || ttl > 25*time.Hour {
		t.Errorf("default TTL=%s, want about 24h", ttl)
	}
	listed := chatShareManagementTestRequest(t, h, http.MethodGet, path, "", userID, wsID)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), response.Token) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var list struct {
		Shares []struct {
			ID string `json:"id"`
		} `json:"shares"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || len(list.Shares) != 1 || list.Shares[0].ID != response.Share.ID {
		t.Fatalf("list body=%s err=%v", listed.Body.String(), err)
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodGet, path, "", userID, wsID, "agents:read"); rr.Code != http.StatusOK {
		t.Fatalf("read-scoped CLI list status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodGet, path, "", userID, wsID, "credentials:read"); rr.Code != http.StatusForbidden {
		t.Fatalf("unrelated CLI scope list status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := chatShareManagementTestRequest(t, h, http.MethodDelete, path+"/"+response.Share.ID, "", userID, wsID, "agents:read"); rr.Code != http.StatusForbidden {
		t.Fatalf("read-scoped CLI revoke status=%d body=%s", rr.Code, rr.Body.String())
	}
	revoked := chatShareManagementTestRequest(t, h, http.MethodDelete, path+"/"+response.Share.ID, "", userID, wsID, "agents:write")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	if grant, err := h.store.Validate(t.Context(), response.Share.ID, response.Token); err == nil {
		t.Fatalf("revoked token remained valid: %+v", grant)
	}
	custom := chatShareManagementTestRequest(t, h, http.MethodPost, path, `{"ttl_seconds":60}`, userID, wsID)
	if custom.Code != http.StatusCreated {
		t.Fatalf("custom TTL status=%d body=%s", custom.Code, custom.Body.String())
	}
	var customResponse struct {
		Share struct {
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"share"`
	}
	if err := json.Unmarshal(custom.Body.Bytes(), &customResponse); err != nil {
		t.Fatal(err)
	}
	if ttl := time.Until(customResponse.Share.ExpiresAt); ttl < 55*time.Second || ttl > 65*time.Second {
		t.Errorf("custom TTL=%s, want about 60s", ttl)
	}
}

func TestChatShareReader_UpstreamTranscriptLimitIsNotAnEmptySuccess(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats/share-chat/shared-messages" {
			t.Errorf("IPC path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	h, _, id, token := chatShareBoundaryFixture(t, sock)
	rr := readSharedChatBoundary(h, id, token, "", "")
	if rr.Code != http.StatusRequestEntityTooLarge || strings.Contains(rr.Body.String(), `"messages":[]`) {
		t.Fatalf("oversize transcript status=%d body=%s", rr.Code, rr.Body.String())
	}
}
