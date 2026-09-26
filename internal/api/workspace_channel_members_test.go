package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

func TestWorkspaceChannelMemberManagementHTTP(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES('channel-member','channel-member@example.test'); INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('channel-membership',?,'channel-member','MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	store := groupchat.New(db)
	room, err := store.Create(t.Context(), ws, owner, groupchat.CreateInput{Title: "Channel members", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewWorkspaceConversationsHandler(store, newTestLogger())
	for _, tt := range []struct {
		method, user  string
		status, count int
	}{
		{"GET", owner, 200, 1}, {"POST", "channel-member", 404, 0}, {"POST", owner, 204, 0},
		{"GET", owner, 200, 2}, {"DELETE", "channel-member", 404, 0}, {"DELETE", owner, 204, 0},
		{"GET", "channel-member", 200, 1},
	} {
		r := httptest.NewRequest(tt.method, "/", strings.NewReader(`{"user_id":"channel-member"}`))
		r.SetPathValue("conversationId", room.ID)
		r.SetPathValue("userId", "channel-member")
		r = r.WithContext(withUser(withWorkspace(r.Context(), ws, "MEMBER"), &AuthUser{ID: tt.user}))
		w := httptest.NewRecorder()
		switch tt.method {
		case "GET":
			h.Members(w, r)
		case "POST":
			h.AddMember(w, r)
		case "DELETE":
			h.RemoveMember(w, r)
		}
		if w.Code != tt.status {
			t.Fatalf("%s %s status=%d body=%s", tt.method, tt.user, w.Code, w.Body.String())
		}
		if tt.method == "GET" {
			var body struct {
				Participants []groupchat.Member `json:"participants"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Participants) != tt.count {
				t.Fatalf("roster %s err=%v", w.Body.String(), err)
			}
		}
	}
}
