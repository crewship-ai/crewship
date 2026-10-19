package api

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Observe the exact marshaled Hub frames, not a mock broadcaster. Workspace
// membership permits this subscriber, but gives no private Page read authority.
func TestBoardPrivateInitializationWorkspaceInvalidationContainsNoIdentity(t *testing.T) {
	h, f, boundary := boardHumanFixture(t)
	mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.wsID, f.adminID)
	hub := newTestHubForStream(t)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.db))
	h.hub = hub
	channel := "workspace:" + f.wsID
	if allowed, err := hub.CanSubscribeChannel(t.Context(), f.adminID, channel); err != nil || !allowed {
		t.Fatalf("unrelated workspace subscriber positive control: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := hub.CanSubscribeChannel(t.Context(), f.adminID, "page:board-human-page"); err != nil || allowed {
		t.Fatalf("private Page must remain outside subscriber audience: allowed=%v err=%v", allowed, err)
	}
	observer := hub.AddObserver(channel, f.adminID, 4)
	defer hub.RemoveObserver(channel, observer)
	initialized := boardHumanCall(t, f, boundary, f.userID, "POST", "@board-human-page", `{"mutation_id":"private-broadcast"}`)
	if initialized.Code != 200 {
		t.Fatalf("initialize: HTTP%d %s", initialized.Code, initialized.Body.String())
	}
	if denied := boardHumanCall(t, f, boundary, f.adminID, "GET", "private-board", ""); denied.Code != 404 {
		t.Fatalf("unrelated subscriber unexpectedly reads private Board: HTTP%d", denied.Code)
	}
	select {
	case raw, open := <-observer.Frames():
		if !open {
			t.Fatal("authorized workspace observer closed before invalidation")
		}
		var envelope struct {
			Type    string         `json:"type"`
			Channel string         `json:"channel"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != "page.updated" || envelope.Channel != channel {
			t.Fatalf("missing workspace invalidation positive control: %s", raw)
		}
		for _, private := range []string{"board-human-page", "private-board", "Private Board title"} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatalf("workspace subscriber received private Board identity %q in raw frame: %s", private, raw)
			}
		}
		if len(envelope.Payload) != 0 {
			t.Fatalf("private Board invalidation must be audience-neutral: %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authorized workspace subscriber received no invalidation")
	}
}
