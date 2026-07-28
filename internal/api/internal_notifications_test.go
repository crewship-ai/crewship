package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedNotifyAgentRows builds the minimum a send needs: a workspace, a crew,
// an agent in it, and one webhook channel.
func seedNotifyAgentRows(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		 VALUES ('ws1', 'WS', 'ws', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`,
		`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		 VALUES ('crew1', 'ws1', 'Crew', 'crew', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, created_at, updated_at)
		 VALUES ('agent1', 'ws1', 'crew1', 'Agent', 'agent', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`,
		`INSERT INTO notification_channels (id, workspace_id, type, config_json, events_json, enabled)
		 VALUES ('nch1', 'ws1', 'webhook', '{"url":"https://receiver.example.com/hook"}', '[]', 1)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed (%s): %v", q[:40], err)
		}
	}
}

// postAgentSend drives POST /api/v1/internal/notifications/send.
func postAgentSend(t *testing.T, h *AgentNotifyHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/notifications/send", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Send(rr, req)
	return rr
}

// TestAgentSend_UnpairedIsRefused is the security property the whole feature
// rests on.
//
// An agent can reach the network anyway; what this gate decides is which of
// the workspace's channels it may speak on. A workspace Slack channel is
// something an admin stood up for a team, on a surface where a reader cannot
// tell an agent's message from a colleague's — so an agent that could post
// there by virtue of existing turns one confused or prompt-injected agent
// into a workspace-wide megaphone. Default-deny, and only a human changes it.
func TestAgentSend_UnpairedIsRefused(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	rr := postAgentSend(t, h, `{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"channel_id": "nch1", "title": "hello"
	}`)

	if rr.Code != 403 {
		t.Fatalf("an unpaired agent must be refused; got %d: %s", rr.Code, rr.Body.String())
	}
	// The refusal has to tell a human what to do — the agent cannot fix this
	// itself, so an opaque 403 just produces a confused retry loop.
	if !strings.Contains(rr.Body.String(), "not paired") {
		t.Errorf("the refusal should explain the pairing requirement, got: %s", rr.Body.String())
	}
}

// TestAgentSend_PairedIsAllowedThrough pins that a granted agent gets past
// authorization. Delivery itself fails (nothing is listening on the webhook
// URL), which is the point: the failure must be at DELIVERY, not at the gate.
func TestAgentSend_PairedIsAllowedThrough(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	if _, err := db.Exec(
		`INSERT INTO notification_channel_agents (id, workspace_id, channel_id, agent_id)
		 VALUES ('nca1', 'ws1', 'nch1', 'agent1')`); err != nil {
		t.Fatal(err)
	}
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	rr := postAgentSend(t, h, `{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"channel_id": "nch1", "title": "hello", "body": "world"
	}`)

	if rr.Code == 403 {
		t.Fatalf("a paired agent must pass authorization; got 403: %s", rr.Body.String())
	}
}

// TestAgentSend_CannotSendAsAnotherAgent pins that the agent id is checked
// against the workspace, not merely taken at its word. Without this the
// pairing table would be decorative: any agent could name a paired sibling's
// id and inherit its grants.
func TestAgentSend_CannotSendAsAnotherAgent(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	rr := postAgentSend(t, h, `{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent-from-another-tenant",
		"channel_id": "nch1", "title": "hello"
	}`)
	if rr.Code != 403 {
		t.Fatalf("an unknown agent id must be refused; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAgentSend_WrongWorkspaceIsRefused pins tenant isolation on this path.
func TestAgentSend_WrongWorkspaceIsRefused(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	rr := postAgentSend(t, h, `{
		"workspace_id": "ws-someone-else", "crew_id": "crew1", "agent_id": "agent1",
		"channel_id": "nch1", "title": "hello"
	}`)
	if rr.Code != 403 {
		t.Fatalf("an agent claiming another workspace must be refused; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAgentSend_RateLimited pins the anti-flood gate. An agent in a loop can
// call a tool hundreds of times a minute without noticing, and the blast
// radius is a channel other people are reading.
func TestAgentSend_RateLimited(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	if _, err := db.Exec(
		`INSERT INTO notification_channel_agents (id, workspace_id, channel_id, agent_id)
		 VALUES ('nca1', 'ws1', 'nch1', 'agent1')`); err != nil {
		t.Fatal(err)
	}
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())
	body := `{"workspace_id":"ws1","crew_id":"crew1","agent_id":"agent1","channel_id":"nch1","title":"spam"}`

	sawRateLimit := false
	for i := 0; i < 12; i++ {
		if postAgentSend(t, h, body).Code == 429 {
			sawRateLimit = true
			break
		}
	}
	if !sawRateLimit {
		t.Error("a looping agent was never rate-limited — one chatty agent could flood a team's channel")
	}
}

// TestAgentSend_RequiresTitle pins that an empty notification is rejected
// rather than delivered as a blank message someone has to go and investigate.
func TestAgentSend_RequiresTitle(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	rr := postAgentSend(t, h, `{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1", "channel_id": "nch1"
	}`)
	if rr.Code != 400 {
		t.Fatalf("a titleless notification must be rejected; got %d", rr.Code)
	}
}

// TestAgentSend_BodyIsCapped pins the size bound: a runaway agent must not be
// able to page a megabyte onto someone's phone.
func TestAgentSend_BodyIsCapped(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	payload, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"channel_id": "nch1", "title": "big", "body": strings.Repeat("x", agentNotifyBodyCap+1),
	})
	rr := postAgentSend(t, h, string(payload))
	if rr.Code != 400 {
		t.Fatalf("an oversized body must be rejected; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAgentListChannels_OnlyShowsPairedOnes pins that discovery is scoped to
// the grants, and that it does not leak a channel's destination address —
// an agent needs to know a channel EXISTS, not where it points.
func TestAgentListChannels_OnlyShowsPairedOnes(t *testing.T) {
	db := setupTestDB(t)
	seedNotifyAgentRows(t, db)
	// A second channel the agent is NOT paired with.
	if _, err := db.Exec(
		`INSERT INTO notification_channels (id, workspace_id, type, config_json, events_json, enabled)
		 VALUES ('nch2', 'ws1', 'webhook', '{"url":"https://secret.example.com/hook"}', '[]', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO notification_channel_agents (id, workspace_id, channel_id, agent_id)
		 VALUES ('nca1', 'ws1', 'nch1', 'agent1')`); err != nil {
		t.Fatal(err)
	}
	h := NewAgentNotifyHandler(db, nil, nil, newTestLogger())

	req := httptest.NewRequest("GET",
		"/api/v1/internal/notifications/channels?workspace_id=ws1&crew_id=crew1&agent_id=agent1", nil)
	rr := httptest.NewRecorder()
	h.ListChannels(rr, req)

	if rr.Code != 200 {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Channels []map[string]any `json:"channels"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if len(body.Channels) != 1 {
		t.Fatalf("expected only the paired channel, got %d: %s", len(body.Channels), rr.Body.String())
	}
	if body.Channels[0]["channel_id"] != "nch1" {
		t.Errorf("wrong channel returned: %v", body.Channels[0])
	}
	if strings.Contains(rr.Body.String(), "secret.example.com") {
		t.Error("the unpaired channel's URL leaked into the agent-facing listing")
	}
	if strings.Contains(rr.Body.String(), "url") {
		t.Errorf("a destination address reached the agent; it should only learn that a channel exists: %s", rr.Body.String())
	}
}
