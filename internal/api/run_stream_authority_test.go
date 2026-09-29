package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

type authorityStreamSource struct {
	*ws.Hub
	authorizer *ws.DBChannelAuthorizer
}

func (s authorityStreamSource) CanSubscribeChannel(ctx context.Context, user, channel string) (bool, error) {
	return s.authorizer.CanSubscribe(ctx, user, channel)
}

func TestRunStreamReauthorizesBeforeEveryDelivery(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	seedAgentRow(t, db, "stream-access-agent", workspace, "", "Agent", "stream-access-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('stream-access-chat',?,'stream-access-agent',?,'private')`, workspace, user)
	hub := newTestHubForStream(t)
	src := authorityStreamSource{hub, ws.NewDBChannelAuthorizer(db)}
	srv := newRunStreamTestServer(t, src, user)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(srv.URL + "/api/v1/chats/stream-access-chat/stream?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "stream.open") {
		t.Fatalf("open: %q %v", line, err)
	}
	hub.Broadcast("session:stream-access-chat", ws.ServerMessage{Type: "chat_event", Channel: "session:stream-access-chat", Seq: 1, Payload: ws.ChatEvent{Type: "text", Content: "positive-control"}})
	line, err = reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "positive-control") {
		t.Fatalf("positive: %q %v", line, err)
	}
	execOrFatal(t, db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace)
	hub.Broadcast("session:stream-access-chat", ws.ServerMessage{Type: "chat_event", Channel: "session:stream-access-chat", Seq: 2, Payload: ws.ChatEvent{Type: "text", Content: "revoked-canary"}})
	remaining, err := io.ReadAll(reader)
	if strings.Contains(string(remaining), "revoked-canary") {
		t.Fatal("post-revocation content delivered")
	}
	if err != nil {
		t.Fatalf("stream failed to close on revocation: %v", err)
	}
}
