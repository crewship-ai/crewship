package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/groupchat"
)

// The IPC fixture reads a real owned JSONL store. It is a component transport,
// not a model/provider or complete application-server acceptance claim.
func TestChatSourceAgentProjectionUsesOriginalAuthorizedStore(t *testing.T) {
	store := conversation.NewStore(t.TempDir(), newTestLogger())
	t.Cleanup(store.Close)
	original := conversation.Message{ID: "original-message", Role: conversation.RoleAssistant, AgentID: "agent-pxc", Content: "Confirmed reply", Timestamp: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC), Parts: []conversation.Part{{Type: "text", Content: "Confirmed reply"}, {Type: "thinking", Content: "private-legacy-thinking-fixture", Metadata: map[string]any{"private": "legacy-metadata-fixture"}}}}
	if err := store.Append(t.Context(), "chat-source", original); err != nil {
		t.Fatal(err)
	}
	calls := 0
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		rows, err := store.ReadShared(r.Context(), "chat-source", 16<<20, 1000)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": rows})
	}))
	h, user, workspace, _, agent := covProxyRig(t, sock)
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,created_by,title)VALUES('chat-source',?,?,?,'Source test')`, agent, workspace, user); err != nil {
		t.Fatal(err)
	}
	invoke := func(query, actor, role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/chats/chat-source/messages"+query, nil)
		r.SetPathValue("chatId", "chat-source")
		r = withWorkspaceUser(r, actor, workspace, role)
		w := httptest.NewRecorder()
		h.ChatMessages(w, r)
		return w
	}
	control := invoke("", user, "OWNER")
	if control.Code != 200 || !strings.Contains(control.Body.String(), "original-message") {
		t.Fatalf("existing authorized control %d", control.Code)
	}
	before := calls
	denied := invoke("?projection=source_v1", user, "")
	if denied.Code != 403 || calls != before {
		t.Fatal("existing read role gate lost")
	}
	unknown := httptest.NewRequest("GET", "/api/v1/chats/chat-source/messages?projection=source_v1", nil)
	unknown.SetPathValue("chatId", "chat-source")
	unauthenticated := httptest.NewRecorder()
	h.ChatMessages(unauthenticated, unknown)
	if unauthenticated.Code != 401 || calls != before {
		t.Fatal("unauthenticated source read reached IPC")
	}
	wrongWorkspace := httptest.NewRequest("GET", "/api/v1/chats/chat-source/messages?projection=source_v1", nil)
	wrongWorkspace.SetPathValue("chatId", "chat-source")
	wrongWorkspace = withWorkspaceUser(wrongWorkspace, user, "other-workspace", "OWNER")
	outside := httptest.NewRecorder()
	h.ChatMessages(outside, wrongWorkspace)
	if outside.Code != 404 || calls != before {
		t.Fatal("cross-workspace source read reached IPC")
	}
	projected := invoke("?projection=source_v1", user, "OWNER")
	if projected.Code != 200 {
		t.Fatalf("projection %d %s", projected.Code, projected.Body.String())
	}
	var body struct {
		Version  int                          `json:"contract_version"`
		Ref      conversation.ConversationRef `json:"conversation_ref"`
		Messages []struct {
			Ref struct {
				SourceMessageID string `json:"source_message_id"`
			} `json:"message_ref"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(projected.Body.Bytes(), &body); err != nil || body.Version != 1 || body.Ref.WorkspaceID != workspace || body.Ref.SourceID != "chat-source" || body.Ref.SourceKind != conversation.SourceAgentSession || len(body.Messages) != 1 || body.Messages[0].Ref.SourceMessageID != original.ID {
		t.Fatalf("original versioned message reference absent: %v %s", err, projected.Body.String())
	}
	if strings.Contains(projected.Body.String(), "private-legacy-thinking-fixture") || strings.Contains(projected.Body.String(), "legacy-metadata-fixture") {
		t.Fatal("source projection disclosed legacy thinking")
	}
	rows, err := store.ReadShared(t.Context(), "chat-source", 16<<20, 1000)
	if err != nil || rows[0].Parts[1].Content != original.Parts[1].Content {
		t.Fatal("read projection rewrote source transcript")
	}
}

func TestChatSourceRoomProjectionPreservesHumanMessageAndNoRun(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	h := NewWorkspaceConversationsHandler(groupchat.New(db), newTestLogger())
	room, err := h.store.Create(t.Context(), workspace, owner, groupchat.CreateInput{Title: "Source room", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	message, duplicate, err := h.store.Send(t.Context(), workspace, owner, room.ID, groupchat.SendInput{ClientID: "stable-human-send", Content: "Ordinary human message"})
	if err != nil || duplicate {
		t.Fatalf("human send control %v", err)
	}
	retry, duplicate, err := h.store.Send(t.Context(), workspace, owner, room.ID, groupchat.SendInput{ClientID: "stable-human-send", Content: "Ordinary human message"})
	if err != nil || !duplicate || retry.ID != message.ID {
		t.Fatal("existing stable send changed")
	}
	var jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, room.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("ordinary human message started agent work")
	}
	r := httptest.NewRequest("GET", "/api/v1/conversations/"+room.ID+"/messages?projection=source_v1", nil)
	r.SetPathValue("conversationId", room.ID)
	r = withWorkspaceUser(r, owner, workspace, "OWNER")
	w := httptest.NewRecorder()
	h.Messages(w, r)
	if w.Code != 200 {
		t.Fatalf("room projection %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Version  int `json:"contract_version"`
		Messages []struct {
			Ref struct {
				SourceMessageID string `json:"source_message_id"`
			} `json:"message_ref"`
			Order struct {
				Sequence int64 `json:"source_sequence"`
			} `json:"order"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Version != 1 || len(body.Messages) != 1 || body.Messages[0].Ref.SourceMessageID != message.ID || body.Messages[0].Order.Sequence != message.Sequence {
		t.Fatalf("native room identity/sequence absent: %v %s", err, w.Body.String())
	}
}

func TestChatSourceSnapshotFailsClosedWithoutPartialRows(t *testing.T) {
	status := 200
	payload := `{"messages":[]}`
	calls := 0
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/chats/chat-source/shared-messages" {
			t.Errorf("wrong bounded reader: %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	h, _, workspace, _, _ := covProxyRig(t, sock)
	rows := make([]conversation.Message, 1001)
	oversizedRows, _ := json.Marshal(map[string]any{"messages": rows})
	for _, tc := range []struct {
		name, query, body string
		status, want      int
	}{
		{"empty", "?projection=source_v1", `{"messages":[]}`, 200, 200},
		{"future", "?projection=source_v2", `{"messages":[]}`, 200, 400},
		{"duplicate selector", "?projection=source_v1&projection=source_v1", `{"messages":[]}`, 200, 400},
		{"fake paging", "?projection=source_v1&offset=1", `{"messages":[]}`, 200, 400},
		{"source overflow", "?projection=source_v1", "private fixture", 413, 413},
		{"upstream unavailable", "?projection=source_v1", "private fixture", 500, 503},
		{"bad json", "?projection=source_v1", "private fixture", 200, 503},
		{"missing messages", "?projection=source_v1", `{}`, 200, 503},
		{"null messages", "?projection=source_v1", `{"messages":null}`, 200, 503},
		{"row overflow", "?projection=source_v1", string(oversizedRows), 200, 413},
		{"wire overflow", "?projection=source_v1", strings.Repeat(" ", 16<<20) + "x", 200, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, payload = tc.status, tc.body
			r := httptest.NewRequest("GET", "/messages"+tc.query, nil)
			r.SetPathValue("chatId", "chat-source")
			r = withWorkspaceUser(r, "unused-human", workspace, "OWNER")
			before := calls
			w := httptest.NewRecorder()
			h.chatSourceMessages(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d want %d", w.Code, tc.want)
			}
			if tc.want == 400 && calls != before {
				t.Fatal("invalid selector reached IPC")
			}
			if tc.want != 200 && strings.Contains(w.Body.String(), "private fixture") {
				t.Fatal("private upstream error echoed")
			}
		})
	}
}
