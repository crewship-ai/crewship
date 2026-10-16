package notifyroute

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
)

func TestVerifiedPrivateTextNotificationRechecksExpandedRecipients(t *testing.T) {
	db := newRouteTestDB(t)
	for _, sql := range []string{
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('private-agent','ws1','Agent','private-agent')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('private-crew','ws1','Crew','private-crew')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('private-chat','ws1','private-agent','u_member','group')`,
		`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,source_run_id,created_at) VALUES('private-source','ws1','private-crew','private-chat','private-agent','private question','TEXT','verified-run','2026-10-16T12:00:00Z')`,
	} {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	srv := newRecordingWebhookServer(t)
	channel := seedWebhookChannel(t, db, srv.URL)
	if err := NewPrefStore(db).Set(context.Background(), "ws1", "u_manager", []PrefCell{{Category: notify.CategoryAgentsEscalation, ChannelID: channel.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(db, nil, nil)
	item := inbox.Item{WorkspaceID: "ws1", Kind: inbox.KindEscalation, SourceID: "private-source", TargetRole: "MANAGER", Title: "private question"}
	router.route(context.Background(), notify.CategoryAgentsEscalation, item)
	if srv.count() != 0 {
		t.Fatal("hidden role expansion delivered private title")
	}
	if _, err := db.Exec(`INSERT INTO chat_participants(chat_id,user_id) VALUES('private-chat','u_manager')`); err != nil {
		t.Fatal(err)
	}
	router.route(context.Background(), notify.CategoryAgentsEscalation, item)
	if srv.count() != 1 {
		t.Fatalf("authorized participant did not receive existing notification: %d", srv.count())
	}
	if _, err := db.Exec(`DELETE FROM chat_participants WHERE chat_id='private-chat' AND user_id='u_manager'`); err != nil {
		t.Fatal(err)
	}
	item.SourceID = "private-source-after-revoke"
	if _, err := db.Exec(`UPDATE escalations SET id=? WHERE id='private-source'`, item.SourceID); err != nil {
		t.Fatal(err)
	}
	router.route(context.Background(), notify.CategoryAgentsEscalation, item)
	if srv.count() != 1 {
		t.Fatal("revoked participant received private title")
	}
	// Source-less service escalation remains eligible under the old role target.
	item.SourceID = "legacy-service-source"
	router.route(context.Background(), notify.CategoryAgentsEscalation, item)
	if srv.count() != 2 {
		t.Fatalf("legacy service notification lost: %d", srv.count())
	}
}
