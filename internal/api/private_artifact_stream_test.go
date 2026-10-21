package api

import (
	"bufio"
	"github.com/crewship-ai/crewship/internal/ws"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPrivateArtifactConsentStopsAlreadyOpenHTTPStream(t *testing.T) {
	db := setupTestDB(t)
	author := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, author)
	const admin = "private-stream-admin"
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,'private-stream-admin@example.test')`, admin)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('private-stream-admin-member',?,?,'ADMIN')`, workspace, admin)
	seedAgentRow(t, db, "private-stream-agent", workspace, "", "Agent", "private-stream-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('private-stream-chat',?,'private-stream-agent',?,'private')`, workspace, author)
	hub := newTestHubForStream(t)
	source := authorityStreamSource{hub, ws.NewDBChannelAuthorizer(db)}
	server := newRunStreamTestServer(t, source, admin)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(server.URL + "/api/v1/chats/private-stream-chat/stream?follow=1&idle=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "stream.open") {
		t.Fatalf("ordinary stream baseline=%q %v", line, err)
	}
	execOrFatal(t, db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('private-stream-consent','private-stream-chat','private-stream-agent','user','create document','2026-10-19T10:00:00Z',?)`, author)
	execOrFatal(t, db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('private-stream-consent',?,?,'private-stream-chat','private-document','2026-10-19T10:00:00Z')`, workspace, author)
	hub.Broadcast("session:private-stream-chat", ws.ServerMessage{Type: "chat_event", Channel: "session:private-stream-chat", Seq: 1, Payload: ws.ChatEvent{Type: "text", Content: "private-output-canary"}})
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "private-output-canary") {
		t.Fatal("already-open ADMIN HTTP stream received private output after consent")
	}
}
