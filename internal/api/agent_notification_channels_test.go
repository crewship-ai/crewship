package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// The channel side has always been able to answer "who may post here?"
// (GET /notification-channels/{id}/agents). The agent side could not answer
// the mirror question — "what can this agent reach?" — even though the store
// has had ListForAgent all along for the notify_send discovery path. Without a
// route, the only way to build that view was to fetch every channel and ask
// each one about this agent: an N+1 that also misses channels the caller
// cannot list. So the crew card could not show that Riley has Gmail.
func TestAgentNotificationChannels_ListsWhatTheAgentMayReach(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	db := setupTestDB(t)
	store := notify.NewChannelStore(db)
	pairings := notify.NewPairingStore(db)
	ctx := context.Background()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w1')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('agent_1', 'ws1', 'Riley', 'riley')`)

	reachable, err := store.Create(ctx, notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelShoutrrr, Provider: "discord",
		ShoutrrrURL: "discord://token@id",
	})
	if err != nil {
		t.Fatal(err)
	}
	unreachable, err := store.Create(ctx, notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelShoutrrr, Provider: "slack",
		ShoutrrrURL: "slack://token@id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pairings.Allow(ctx, "ws1", reachable.ID, "agent_1", "u1"); err != nil {
		t.Fatal(err)
	}

	h := NewAgentNotifyChannelsHandler(db, newTestLogger())
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/agents/agent_1/notification-channels", nil),
		"u1", "ws1", "MEMBER",
	)
	req.SetPathValue("agentId", "agent_1")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("got %d channels, want only the paired one: %s", len(body.Channels), rr.Body.String())
	}
	if body.Channels[0]["id"] != reachable.ID {
		t.Errorf("returned %v, want the paired channel %s", body.Channels[0]["id"], reachable.ID)
	}
	if body.Channels[0]["provider"] != "discord" {
		t.Errorf("provider = %v, want discord", body.Channels[0]["provider"])
	}
	_ = unreachable
}

// An agent has no business learning a channel's destination — a Telegram chat
// id or a webhook URL is a contact detail, and this route is readable by any
// member. The store deliberately does not unmarshal config_json for this path;
// this pins that the handler does not reintroduce it.
func TestAgentNotificationChannels_NeverLeaksTheDestination(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	db := setupTestDB(t)
	store := notify.NewChannelStore(db)
	pairings := notify.NewPairingStore(db)
	ctx := context.Background()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w1')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('agent_1', 'ws1', 'Riley', 'riley')`)

	ch, err := store.Create(ctx, notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook,
		URL: "https://secret.example/hooks/abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pairings.Allow(ctx, "ws1", ch.ID, "agent_1", "u1"); err != nil {
		t.Fatal(err)
	}

	h := NewAgentNotifyChannelsHandler(db, newTestLogger())
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/agents/agent_1/notification-channels", nil),
		"u1", "ws1", "MEMBER",
	)
	req.SetPathValue("agentId", "agent_1")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if got := rr.Body.String(); contains(got, "secret.example") || contains(got, "abc123") {
		t.Errorf("response leaked the destination: %s", got)
	}
}

func TestAgentNotificationChannels_RequiresAnAgentID(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentNotifyChannelsHandler(db, newTestLogger())
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/agents//notification-channels", nil),
		"u1", "ws1", "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// An agent with no pairings must come back as an empty list, not null — a
// client that renders `channels.length` should show "none", not crash.
func TestAgentNotificationChannels_EmptyIsAList(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentNotifyChannelsHandler(db, newTestLogger())
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/agents/agent_nobody/notification-channels", nil),
		"u1", "ws1", "MEMBER",
	)
	req.SetPathValue("agentId", "agent_nobody")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var body struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Channels == nil {
		t.Error("channels came back null; want an empty array")
	}
}
