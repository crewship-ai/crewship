package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// seedTwoPersonalChannels gives u1 and u2 one personal channel each, plus one
// workspace channel.
func seedTwoPersonalChannels(t *testing.T, h *NotifyChannelHandler) {
	t.Helper()
	stmts := []string{
		`INSERT INTO notification_channels (id, workspace_id, type, config_json, events_json, enabled, scope, owner_user_id)
		 VALUES ('nch_ws', 'ws1', 'webhook', '{"url":"https://team.example.com/hook"}', '[]', 1, 'workspace', NULL)`,
		`INSERT INTO notification_channels (id, workspace_id, type, config_json, events_json, enabled, scope, owner_user_id)
		 VALUES ('nch_u1', 'ws1', 'webhook', '{"url":"https://alice.example.com/hook"}', '[]', 1, 'user', 'u1')`,
		`INSERT INTO notification_channels (id, workspace_id, type, config_json, events_json, enabled, scope, owner_user_id)
		 VALUES ('nch_u2', 'ws1', 'email', '{"to":"bob-private@example.com"}', '[]', 1, 'user', 'u2')`,
	}
	for _, q := range stmts {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func listChannels(t *testing.T, h *NotifyChannelHandler, role, query string) []notify.Channel {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-channels"+query, nil), "u1", "ws1", role)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list: got %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Channels []notify.Channel `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Channels
}

// TestChannelList_DefaultHidesOtherMembersPersonalChannels pins the existing
// privacy boundary, which the admin overview must not weaken by accident.
func TestChannelList_DefaultHidesOtherMembersPersonalChannels(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())
	seedTwoPersonalChannels(t, h)

	for _, ch := range listChannels(t, h, "ADMIN", "") {
		if ch.ID == "nch_u2" {
			t.Error("the default listing leaked another member's personal channel")
		}
	}
}

// TestChannelList_AdminScopeSeesEveryConnection pins the admin overview: one
// page that answers "what is this instance wired into". Without it an admin
// cannot see that a member has quietly pointed a channel somewhere.
func TestChannelList_AdminScopeSeesEveryConnection(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())
	seedTwoPersonalChannels(t, h)

	ids := map[string]bool{}
	for _, ch := range listChannels(t, h, "ADMIN", "?scope=all") {
		ids[ch.ID] = true
	}
	for _, want := range []string{"nch_ws", "nch_u1", "nch_u2"} {
		if !ids[want] {
			t.Errorf("admin overview is missing channel %q", want)
		}
	}
}

// TestChannelList_AdminScopeRedactsOthersDestinations pins that the overview
// shows THAT a member has a channel, not WHERE it points. A Telegram chat id
// or a private email address is the owner's contact detail, not workspace
// configuration an admin is entitled to read.
func TestChannelList_AdminScopeRedactsOthersDestinations(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())
	seedTwoPersonalChannels(t, h)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-channels?scope=all", nil), "u1", "ws1", "ADMIN")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	raw := rr.Body.String()

	if strings.Contains(raw, "bob-private@example.com") {
		t.Error("another member's personal destination address leaked into the admin overview")
	}
	// The caller's OWN personal channel keeps its destination — they own it.
	if !strings.Contains(raw, "alice.example.com") {
		t.Error("the admin's own personal channel was redacted; only other members' should be")
	}
	// Workspace channels are shared configuration and stay visible.
	if !strings.Contains(raw, "team.example.com") {
		t.Error("a workspace channel's destination was redacted; it is shared configuration")
	}
}

// TestChannelList_MemberCannotEscalateViaScopeParam pins that the query
// parameter is not the authorization — a member who guesses ?scope=all gets
// their own view, not everyone's.
func TestChannelList_MemberCannotEscalateViaScopeParam(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())
	seedTwoPersonalChannels(t, h)

	for _, ch := range listChannels(t, h, "MEMBER", "?scope=all") {
		if ch.ID == "nch_u2" {
			t.Error("a MEMBER escalated to the admin overview by adding ?scope=all")
		}
	}
}
