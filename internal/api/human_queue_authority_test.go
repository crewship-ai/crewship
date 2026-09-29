package api

import (
	"encoding/base64"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/ws"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestQueuedHumanAuthorityCannotSurviveRejoin(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('queue-crew',?,'Queue','queue')`, workspace)
	seedAgentRow(t, db, "queue-agent", workspace, "queue-crew", "Agent", "queue-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('queue-human','queue-human@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('original-member',?,'queue-human','MEMBER')`, workspace)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('queue-chat',?,'queue-agent','queue-human','private')`, workspace)
	const master = "synthetic-queue-host-token"
	router, err := NewRouter(db, "synthetic-queue-session-secret-32-chars", newTestLogger(), WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	request := func(receipt string, want int) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/queue-chat/resolve-human?user_id=queue-human&receipt="+url.QueryEscape(receipt), nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", master)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("status=%d want=%d", rr.Code, want)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	initial := request("", 200)
	raw := initial["human_authority"]
	if len(raw) == 0 {
		t.Fatal("host admission did not issue an authority receipt")
	}
	receipt := base64.RawURLEncoding.EncodeToString(raw)
	request(receipt, 200)
	execOrFatal(t, db, `DELETE FROM workspace_members WHERE id='original-member'`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replacement-member',?,'queue-human','MEMBER')`, workspace)
	request(receipt, 404)
	request("", 200) // A genuinely new human send may use the new membership.
	for _, tc := range []struct{ name, mutation string }{
		{"role restored", `UPDATE workspace_members SET role='VIEWER' WHERE id='replacement-member'; UPDATE workspace_members SET role='MEMBER' WHERE id='replacement-member'`},
		{"audience restored", `UPDATE chats SET visibility='group' WHERE id='queue-chat'; UPDATE chats SET visibility='private' WHERE id='queue-chat'`},
		{"agent restored", `UPDATE agents SET deleted_at='2026-09-29T12:00:00Z' WHERE id='queue-agent'; UPDATE agents SET deleted_at=NULL WHERE id='queue-agent'`},
		{"workspace restored", `UPDATE workspaces SET deleted_at='2026-09-29T12:00:00Z' WHERE id='` + workspace + `'; UPDATE workspaces SET deleted_at=NULL WHERE id='` + workspace + `'`},
		{"chat recreated", `DELETE FROM chats WHERE id='queue-chat'; INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('queue-chat','` + workspace + `','queue-agent','queue-human','private')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := request("", 200)["human_authority"]
			old := base64.RawURLEncoding.EncodeToString(raw)
			execOrFatal(t, db, tc.mutation)
			request(old, 404)
			request("", 200)
		})
	}
	// Participant removal/re-add cannot renew queued authority either.
	execOrFatal(t, db, `UPDATE chats SET visibility='group',created_by=? WHERE id='queue-chat'`, owner)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('queue-chat','queue-human','member')`)
	old := base64.RawURLEncoding.EncodeToString(request("", 200)["human_authority"])
	execOrFatal(t, db, `DELETE FROM chat_participants WHERE chat_id='queue-chat' AND user_id='queue-human'; INSERT INTO chat_participants(chat_id,user_id,role) VALUES('queue-chat','queue-human','member')`)
	request(old, 404)
	request("", 200)
	for _, bad := range []string{"%%%", base64.RawURLEncoding.EncodeToString([]byte("null")), base64.RawURLEncoding.EncodeToString([]byte("{}"))} {
		request(bad, 404)
	}
}

func TestPendingMessagesDoNotCoalesceDifferentAuthorities(t *testing.T) {
	h, _, crew, _ := resumeTestRig(t, "")
	h.jobs[crew] = &ProvisionJob{CrewID: crew, Status: "running"}
	send := func(user, content string, revision int64) {
		t.Helper()
		if !h.AttachPendingMessage(crew, chatbridge.PendingChatMessage{UserID: user, ChatID: "same-chat", Content: content, Opts: ws.ChatMessageOption{HumanAuthority: &chataudience.Receipt{UserID: user, MemberRevision: revision}}}) {
			t.Fatal("attach failed")
		}
	}
	send("one", "first", 1)
	send("two", "second", 1)
	send("one", "resend", 1)
	send("one", "new epoch", 2)
	pending := h.jobs[crew].Pending
	if len(pending) != 3 {
		t.Fatalf("pending=%d want=3", len(pending))
	}
	found := map[string]bool{}
	for _, msg := range pending {
		found[msg.Content] = true
	}
	if found["first"] || !found["second"] || !found["resend"] || !found["new epoch"] {
		t.Fatalf("coalesced across authority: %v", found)
	}
}

func TestProvisioningResumePreservesOriginalAuthority(t *testing.T) {
	h, _, _, _ := resumeTestRig(t, "")
	fake := newFakeChatResumer()
	h.chatResumer = fake
	receipt := &chataudience.Receipt{UserID: "human", ChatID: "chat", MemberID: "original", MemberRevision: 4}
	h.resumeMessage(chatbridge.PendingChatMessage{UserID: "human", ChatID: "chat", Content: "original", Opts: ws.ChatMessageOption{HumanAuthority: receipt}}, nil)
	select {
	case call := <-fake.called:
		if !call.opts.HumanResume || call.opts.HumanAuthority == nil || *call.opts.HumanAuthority != *receipt {
			t.Fatal("resume omitted or refreshed admission fence")
		}
	default:
		t.Fatal("positive resume never reached the bridge")
	}
}
