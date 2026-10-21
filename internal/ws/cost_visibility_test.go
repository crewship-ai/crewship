package ws

import (
	"context"
	"strings"
	"testing"
)

// #3590: live frames (journal firehose, routine step/run events, chat run
// metadata) carry money. Below OWNER/ADMIN of the channel's workspace they
// are delivered without it; OWNER/ADMIN get the frame unchanged.
func TestCostVisibleFrame_RoleMatrix(t *testing.T) {
	db := seededDB(t)
	authz := NewDBChannelAuthorizer(db)
	frames := map[string]string{
		"journal":   `{"type":"journal.entry","channel":"journal:ws-1","payload":{"entry_type":"cost.incurred","summary":"$42.0000 spent on a/b","payload":{"cost_usd":42}}}`,
		"workspace": `{"type":"pipeline.step.completed","channel":"workspace:ws-1","payload":{"step_id":"s","cost_usd":42}}`,
		"crew":      `{"type":"x","channel":"crew:crew-live","payload":{"cost_usd":42}}`,
		"agent":     `{"type":"x","channel":"agent:agent-live","payload":{"cost_usd":42}}`,
		"mission":   `{"type":"x","channel":"mission:mission-1","payload":{"total_estimated_cost":42}}`,
		"session":   `{"type":"chat_event","channel":"session:chat-1","payload":{"type":"done","metadata":{"total_cost_usd":42}}}`,
	}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} { // the members table has no GUEST role
		if _, err := db.Exec(`UPDATE workspace_members SET role=? WHERE user_id='u-good'`, role); err != nil {
			t.Fatal(err)
		}
		c := &Client{userID: "u-good", ctx: context.Background()}
		for name, frame := range frames {
			out, ok := c.costVisibleFrame(authz, []byte(frame))
			if !ok {
				t.Fatalf("%s/%s: frame dropped", role, name)
			}
			admin := role == "OWNER" || role == "ADMIN"
			if admin && string(out) != frame {
				t.Errorf("%s/%s: admin frame changed: %s", role, name, out)
			}
			if !admin && (strings.Contains(string(out), "42") || !strings.Contains(string(out), `"type"`)) {
				t.Errorf("%s/%s: want the frame without amounts, got %s", role, name, out)
			}
		}
	}
	// A stranger to the workspace never reads costs either.
	c := &Client{userID: "u-bad", ctx: context.Background()}
	if out, _ := c.costVisibleFrame(authz, []byte(frames["workspace"])); strings.Contains(string(out), "42") {
		t.Errorf("non-member got amount: %s", out)
	}
	// Frames without money pass untouched and without a lookup.
	if out, ok := c.costVisibleFrame(authz, []byte(`{"type":"ping"}`)); !ok || string(out) != `{"type":"ping"}` {
		t.Errorf("plain frame changed: %s", out)
	}
}
