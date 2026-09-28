package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

type chatAudienceRoundTripper struct{ calls int }

func (rt *chatAudienceRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"messages":[{"content":"visible"}]}`))}, nil
}

func TestPrivateChatAudienceAcrossListHistoryAndSession(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	other := "chat-audience-other"
	operator := "chat-audience-operator"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'chat-audience-other@example.com', 'Other')`, other)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('chat-audience-member', ?, ?, 'MEMBER')`, workspace, other)
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'chat-audience-operator@example.com', 'Operator')`, operator)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('chat-audience-operator-member', ?, ?, 'OWNER')`, workspace, operator)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('chat-audience-crew', ?, 'Crew', 'chat-audience-crew')`, workspace)
	seedAgentRow(t, db, "chat-audience-agent", workspace, "chat-audience-crew", "Agent", "chat-audience-agent", "AGENT")
	for _, c := range []struct{ id, title, creator, visibility, origin string }{
		{"chat-owner", "owner-canary", owner, "private", "UI"},
		{"chat-other", "other-canary", other, "private", "UI"},
		{"chat-group", "group-canary", owner, "group", "UI"},
		{"chat-system", "system-run", "", "private", "ROUTINE"},
		{"chat-cron", "cron-run", "", "private", "CRON"},
		{"chat-webhook", "webhook-run", "", "private", "WEBHOOK"},
		{"chat-agent", "delegated-work", "", "private", "AGENT"},
		{"chat-legacy", "unattributed-private", "", "private", ""},
		{"chat-unknown-visibility", "unknown-visibility", owner, "unexpected", "UI"},
	} {
		var creator any = c.creator
		if c.creator == "" {
			creator = nil
		}
		execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, created_by, title, visibility, origin, status) VALUES (?, 'chat-audience-agent', ?, ?, ?, ?, ?, 'ACTIVE')`, c.id, workspace, creator, c.title, c.visibility, c.origin)
	}
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, created_by, title, visibility, mode, status) VALUES ('chat-mission', 'chat-audience-agent', ?, NULL, 'mission-work', 'private', 'MISSION', 'ACTIVE')`, workspace)
	execOrFatal(t, db, `INSERT INTO chat_participants (chat_id, user_id, role) VALUES ('chat-group', ?, 'member')`, other)
	list := NewAgentHandler(db, newTestLogger())
	proxy := NewProxyHandler(db, newTestLogger(), "/unused-chat-audience.sock")
	reactions := NewMessageReactionsHandler(db, newTestLogger())
	participants := NewChatParticipantsHandler(db, newTestLogger())
	upstream := &chatAudienceRoundTripper{}
	proxy.client = &http.Client{Transport: upstream}
	channel := ws.NewDBChannelAuthorizer(db)

	request := func(userID, query string) ([]chatUnreadRow, *httptest.ResponseRecorder) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/chat-audience-agent/chats"+query, nil)
		req.SetPathValue("agentId", "chat-audience-agent")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), workspace, "MEMBER"))
		rr := httptest.NewRecorder()
		list.ListChats(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("list %s: status=%d body=%s", userID, rr.Code, rr.Body.String())
		}
		var rows []chatUnreadRow
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows, rr
	}
	contains := func(rows []chatUnreadRow, chatID string) bool {
		for _, row := range rows {
			if row.ID == chatID {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct{ user, own, foreign string }{{owner, "chat-owner", "chat-other"}, {other, "chat-other", "chat-owner"}} {
		wantRows, wantTotal, wantCounts := 2, "2", "direct=2,routine=0,issue=0,agent=0"
		rows, rr := request(tc.user, "?counts=1&limit=20")
		if len(rows) != wantRows || !contains(rows, tc.own) || contains(rows, tc.foreign) || contains(rows, "chat-legacy") || contains(rows, "chat-unknown-visibility") || rr.Header().Get("X-Total-Count") != wantTotal || rr.Header().Get(ChatKindCountsHeader) != wantCounts {
			t.Errorf("user %s: rows=%+v total=%q counts=%q", tc.user, rows, rr.Header().Get("X-Total-Count"), rr.Header().Get(ChatKindCountsHeader))
		}
		rows, rr = request(tc.user, "?q="+strings.TrimPrefix(tc.foreign, "chat-")+"-canary")
		if len(rows) != 0 || rr.Header().Get("X-Total-Count") != "0" {
			t.Errorf("foreign search leaked to %s: %+v total=%q", tc.user, rows, rr.Header().Get("X-Total-Count"))
		}
		rows, rr = request(tc.user, "?chat_id="+tc.foreign)
		if len(rows) != 0 || rr.Header().Get("X-Total-Count") != "0" {
			t.Errorf("foreign id leaked to %s: %+v total=%q", tc.user, rows, rr.Header().Get("X-Total-Count"))
		}
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			var body io.Reader
			if method == http.MethodPatch {
				body = strings.NewReader(`{"title":"changed"}`)
			}
			mutation := httptest.NewRequest(method, "/api/v1/agents/chat-audience-agent/chats/"+tc.foreign, body)
			mutation.SetPathValue("agentId", "chat-audience-agent")
			mutation.SetPathValue("chatId", tc.foreign)
			mutation = mutation.WithContext(withWorkspace(withUser(mutation.Context(), &AuthUser{ID: tc.user}), workspace, "MEMBER"))
			response := httptest.NewRecorder()
			if method == http.MethodPatch {
				list.UpdateChat(response, mutation)
			} else {
				list.DeleteChat(response, mutation)
			}
			if response.Code != http.StatusNotFound {
				t.Errorf("foreign chat mutation user=%s method=%s status=%d", tc.user, method, response.Code)
			}
		}
		for _, chatID := range []string{tc.own, tc.foreign} {
			wantStatus := http.StatusOK
			if chatID == tc.foreign {
				wantStatus = http.StatusNotFound
			}
			attachmentReq := httptest.NewRequest(http.MethodGet, "/api/v1/agents/chat-audience-agent/chats/"+chatID+"/attachments", nil)
			attachmentReq.SetPathValue("agentId", "chat-audience-agent")
			attachmentReq.SetPathValue("chatId", chatID)
			attachmentReq = attachmentReq.WithContext(withWorkspace(withUser(attachmentReq.Context(), &AuthUser{ID: tc.user}), workspace, "MEMBER"))
			attachmentResponse := httptest.NewRecorder()
			proxy.ListAgentChatAttachments(attachmentResponse, attachmentReq)
			if attachmentResponse.Code != wantStatus {
				t.Errorf("attachment list user=%s chat=%s status=%d want=%d", tc.user, chatID, attachmentResponse.Code, wantStatus)
			}

			readReq := httptest.NewRequest(http.MethodPut, "/api/v1/agents/chat-audience-agent/chats/"+chatID+"/read", nil)
			readReq.SetPathValue("agentId", "chat-audience-agent")
			readReq.SetPathValue("chatId", chatID)
			readReq = readReq.WithContext(withWorkspace(withUser(readReq.Context(), &AuthUser{ID: tc.user}), workspace, "MEMBER"))
			readResponse := httptest.NewRecorder()
			list.MarkChatRead(readResponse, readReq)
			if readResponse.Code != wantStatus {
				t.Errorf("mark-read user=%s chat=%s status=%d want=%d", tc.user, chatID, readResponse.Code, wantStatus)
			}
			for _, endpoint := range []struct {
				name  string
				serve func(http.ResponseWriter, *http.Request)
			}{
				{"reactions", reactions.List},
				{"participants", participants.List},
			} {
				path := "/api/v1/chats/" + chatID + "/" + endpoint.name
				if endpoint.name == "reactions" {
					path = "/api/v1/chats/" + chatID + "/messages/m/reactions"
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.SetPathValue("chatId", chatID)
				req.SetPathValue("messageId", "m")
				req = req.WithContext(withUser(req.Context(), &AuthUser{ID: tc.user}))
				response := httptest.NewRecorder()
				endpoint.serve(response, req)
				if response.Code != wantStatus {
					t.Errorf("%s user=%s chat=%s status=%d want=%d", endpoint.name, tc.user, chatID, response.Code, wantStatus)
				}
			}
		}
		for _, chatID := range []string{tc.own, "chat-group", "chat-system", "chat-cron", "chat-webhook", "chat-agent", "chat-mission", "chat-legacy", "chat-unknown-visibility", tc.foreign} {
			want := chatID == tc.own || chatID == "chat-group"
			allowed, err := channel.CanSubscribe(t.Context(), tc.user, "session:"+chatID)
			if err != nil || allowed != want {
				t.Errorf("session user=%s chat=%s allowed=%v err=%v want=%v", tc.user, chatID, allowed, err, want)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/chats/"+chatID+"/messages", nil)
			req.SetPathValue("chatId", chatID)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: tc.user}), workspace, "MEMBER"))
			rr := httptest.NewRecorder()
			before := upstream.calls
			proxy.ChatMessages(rr, req)
			if want {
				if rr.Code != http.StatusOK || upstream.calls != before+1 {
					t.Errorf("history user=%s chat=%s status=%d calls=%d", tc.user, chatID, rr.Code, upstream.calls-before)
				}
			} else if rr.Code != http.StatusNotFound || upstream.calls != before {
				t.Errorf("foreign history user=%s status=%d calls=%d", tc.user, rr.Code, upstream.calls-before)
			}
		}
	}
	operatorRows, operatorResponse := request(operator, "?counts=1&limit=20")
	if len(operatorRows) != 8 || operatorResponse.Header().Get("X-Total-Count") != "8" || operatorResponse.Header().Get(ChatKindCountsHeader) != "direct=3,routine=3,issue=1,agent=1" {
		t.Errorf("operator work audience: rows=%+v total=%q counts=%q", operatorRows, operatorResponse.Header().Get("X-Total-Count"), operatorResponse.Header().Get(ChatKindCountsHeader))
	}
	for _, tc := range []struct {
		user string
		want int
	}{{owner, 2}, {other, 2}, {operator, 8}} {
		for _, endpoint := range []struct {
			name  string
			serve func(http.ResponseWriter, *http.Request)
		}{
			{"agent list", list.List},
			{"agent detail", list.Get},
		} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/chat-audience-agent", nil)
			req.SetPathValue("agentId", "chat-audience-agent")
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: tc.user}), workspace, "MEMBER"))
			rr := httptest.NewRecorder()
			endpoint.serve(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("%s user=%s status=%d body=%s", endpoint.name, tc.user, rr.Code, rr.Body.String())
				continue
			}
			var agents []agentResponse
			if endpoint.name == "agent list" {
				if err := json.Unmarshal(rr.Body.Bytes(), &agents); err != nil {
					t.Fatal(err)
				}
			} else {
				var agent agentResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &agent); err != nil {
					t.Fatal(err)
				}
				agents = []agentResponse{agent}
			}
			if len(agents) != 1 || agents[0].Count.Chats != tc.want {
				t.Errorf("%s user=%s agents=%+v want chat count %d", endpoint.name, tc.user, agents, tc.want)
			}
		}
	}
	for _, chatID := range []string{"chat-system", "chat-cron", "chat-webhook", "chat-agent", "chat-mission", "chat-legacy", "chat-owner", "chat-other"} {
		want := chatID != "chat-legacy"
		allowed, err := channel.CanSubscribe(t.Context(), operator, "session:"+chatID)
		if err != nil || allowed != want {
			t.Errorf("operator session %s: allowed=%v err=%v want=%v", chatID, allowed, err, want)
		}
	}
	// Removing a participant revokes the next direct read and WS subscription.
	execOrFatal(t, db, `DELETE FROM chat_participants WHERE chat_id='chat-group' AND user_id=?`, other)
	if allowed, err := channel.CanSubscribe(t.Context(), other, "session:chat-group"); err != nil || allowed {
		t.Errorf("revoked participant kept session access: %v %v", allowed, err)
	}
	rows, _ := request(other, "?chat_id=chat-group")
	if len(rows) != 0 {
		t.Errorf("revoked participant kept list access: %+v", rows)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/chats/chat-group/messages", nil)
	req.SetPathValue("chatId", "chat-group")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: other}), workspace, "MEMBER"))
	rr := httptest.NewRecorder()
	before := upstream.calls
	proxy.ChatMessages(rr, req)
	if rr.Code != http.StatusNotFound || upstream.calls != before {
		t.Errorf("revoked participant kept history access: status=%d calls=%d", rr.Code, upstream.calls-before)
	}
}
