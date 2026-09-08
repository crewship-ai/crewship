package api

import (
	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/journal"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceConversationActivityHTTP(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES('activity-peer','activity-peer@example.test'),('activity-outsider','activity-outsider@example.test'); INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('activity-member',?,'activity-peer','MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	s := groupchat.New(db)
	c, err := s.Create(t.Context(), ws, owner, groupchat.CreateInput{Title: "Activity", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewWorkspaceConversationsHandler(s, newTestLogger())
	for _, tt := range []struct {
		name, user, method, body string
		code                     int
	}{
		{"read defaults", "activity-peer", "GET", "", 200},
		{"outsider", "activity-outsider", "GET", "", 404},
		{"member cannot subscribe", "activity-peer", "PUT", `{"issues":true,"routines":true}`, 404},
		{"creator subscribes", owner, "PUT", `{"issues":true,"routines":false}`, 200},
		{"unknown fields", owner, "PUT", `{"issues":true,"backfill":true}`, 400},
		{"wrong types", owner, "PUT", `{"issues":"yes"}`, 400},
		{"unauthenticated", "", "GET", "", 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/", strings.NewReader(tt.body))
			r.SetPathValue("conversationId", c.ID)
			ctx := withWorkspace(r.Context(), ws, "MEMBER")
			if tt.user != "" {
				ctx = withUser(ctx, &AuthUser{ID: tt.user})
			}
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()
			if tt.method == "GET" {
				h.Activity(w, r)
			} else {
				h.SetActivity(w, r)
			}
			if w.Code != tt.code {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// Drive the actual issue endpoint, chained journal writer and projection. This
// catches payload drift that fabricated journal rows cannot detect.
func TestWorkspaceActivityRealIssueStatusProducer(t *testing.T) {
	h, owner, ws, crew, lead, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	h.SetJournal(writer)
	store := groupchat.New(h.db)
	channel, err := store.Create(t.Context(), ws, owner, groupchat.CreateInput{Title: "Actual status events", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetActivity(t.Context(), ws, owner, channel.ID, groupchat.ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"status":"TODO"}`))
	r.SetPathValue("crewId", crew)
	r.SetPathValue("identifier", "ENG-1")
	r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: owner}), ws, "OWNER"))
	response := httptest.NewRecorder()
	h.Update(response, r)
	if response.Code != 200 {
		t.Fatalf("update %d %s", response.Code, response.Body.String())
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = store.ProjectActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages(t.Context(), ws, owner, channel.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].SourceKind != "activity" || !strings.Contains(messages[0].Content, "TODO") || !strings.Contains(messages[0].Content, "/issues/ENG-1?") {
		t.Fatalf("real status missing from activity: %#v", messages)
	}
	if err = store.ProjectActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, channel.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("activity invoked models: %d %v", jobs, err)
	}
}
