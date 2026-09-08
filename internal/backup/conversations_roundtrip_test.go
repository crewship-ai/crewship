package backup_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/groupchat"
)

func TestWorkspaceConversationsBackupRoundTripAndSafeWork(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	w := seedWorkspace(t, source)
	for _, u := range []string{"chat-owner", "chat-peer"} {
		if _, err := source.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, u, u+"@example.test", u); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id) VALUES(?,?,?)`, "wm-"+u, w, u); err != nil {
			t.Fatal(err)
		}
	}
	s := groupchat.New(source)
	private, err := s.Create(ctx, w, "chat-owner", groupchat.CreateInput{Title: "Private history", Kind: "group", MemberIDs: []string{"chat-peer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetMuted(ctx, w, "chat-peer", private.ID, true); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Send(ctx, w, "chat-owner", private.ID, groupchat.SendInput{ClientID: "human", Content: "Private conversation survives restore"})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := s.Create(ctx, w, "chat-owner", groupchat.CreateInput{Title: "Agent channel", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActivity(ctx, w, "chat-owner", channel.ID, groupchat.ActivitySettings{Issues: true, Routines: true}); err != nil {
		t.Fatal(err)
	}
	var agent string
	if err = source.QueryRow(`SELECT id FROM agents WHERE workspace_id=? LIMIT 1`, w).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err = s.AddAgent(ctx, w, "chat-owner", channel.ID, agent); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, w, "chat-owner", channel.ID, groupchat.SendInput{ClientID: "agent", Content: "Queued request", MentionedAgentIDs: []string{agent}}); err != nil {
		t.Fatal(err)
	}
	// A queued assignment must not resume merely because a backup was restored.
	if _, err = source.Exec(`INSERT INTO chats(id,agent_id,workspace_id) VALUES('backup-chat',?,?)`, agent, w); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES('backup-assignment',?,'backup-chat',?,?,'Do not execute after restore','QUEUED')`, w, agent, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`UPDATE workspace_conversation_agent_jobs SET state='queued',assignment_id='backup-assignment'`); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace_conversations", "workspace_conversation_members", "workspace_conversation_messages", "workspace_conversation_agents", "workspace_conversation_agent_jobs", "workspace_conversation_outbox", "workspace_conversation_activity"} {
		if len(dump.Tables[name]) == 0 {
			t.Fatalf("missing backup table %s", name)
		}
	}
	target := openMigratedDB(t)
	if err = backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var restoredActivity int
	if err = target.QueryRow(`SELECT issues+routines+issues_cursor+routines_cursor FROM workspace_conversation_activity WHERE conversation_id=?`, channel.ID).Scan(&restoredActivity); err != nil || restoredActivity != 0 {
		t.Fatalf("restored subscription active: %d %v", restoredActivity, err)
	}
	var content string
	if err = target.QueryRow(`SELECT content FROM workspace_conversation_messages WHERE id=?`, m.ID).Scan(&content); err != nil || content != m.Content {
		t.Fatalf("content %q %v", content, err)
	}
	var muted int
	if err = target.QueryRow(`SELECT muted FROM workspace_conversation_members WHERE conversation_id=? AND user_id='chat-peer'`, private.ID).Scan(&muted); err != nil || muted != 1 {
		t.Fatalf("mute %d %v", muted, err)
	}
	var kind string
	if err = target.QueryRow(`SELECT kind FROM workspace_conversations WHERE id=?`, private.ID).Scan(&kind); err != nil || kind != "group" {
		t.Fatalf("kind %q %v", kind, err)
	}
	var jobState, assignmentState string
	if err = target.QueryRow(`SELECT state FROM workspace_conversation_agent_jobs`).Scan(&jobState); err != nil || jobState != "failed" {
		t.Fatalf("job %q %v", jobState, err)
	}
	if err = target.QueryRow(`SELECT status FROM assignments WHERE id='backup-assignment'`).Scan(&assignmentState); err != nil || assignmentState != "CANCELLED" {
		t.Fatalf("assignment %q %v", assignmentState, err)
	}
	pending, err := groupchat.New(target).PendingEvents(ctx, 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("restored events would replay %+v %v", pending, err)
	}
	restoredStore := groupchat.New(target)
	got, err := restoredStore.Get(ctx, w, "chat-peer", private.ID)
	if err != nil || !got.Muted || got.UnreadCount != 1 {
		t.Fatalf("restored ACL/read %+v %v", got, err)
	}
	if _, _, err = restoredStore.Send(ctx, w, "chat-owner", private.ID, groupchat.SendInput{ClientID: "after-restore", Content: "New activity still delivers"}); err != nil {
		t.Fatal(err)
	}
	pending, err = restoredStore.PendingEvents(ctx, 100)
	if err != nil || len(pending) != 1 {
		t.Fatalf("future outbox %+v %v", pending, err)
	}
	// Restore must not mutate the reusable source dump; fork it independently.
	if dump.Tables["workspace_conversation_agent_jobs"][0]["state"] != "queued" {
		t.Fatal("restore mutated dump")
	}
	if err = backup.RemapIDs(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var forkW, forkPrivate, forkAgent string
	for _, row := range dump.Tables["workspaces"] {
		forkW, _ = row["id"].(string)
		row["slug"] = "forked-conversation-ws"
	}
	for _, row := range dump.Tables["workspace_conversations"] {
		if row["title"] == "Private history" {
			forkPrivate, _ = row["id"].(string)
		}
	}
	for _, row := range dump.Tables["workspace_conversation_messages"] {
		if row["client_id"] == "agent" {
			var ids []string
			if err = json.Unmarshal([]byte(row["mentioned_agent_ids_json"].(string)), &ids); err != nil {
				t.Fatal(err)
			}
			forkAgent = ids[0]
		}
	}
	if forkW == w || forkPrivate == private.ID || forkAgent == agent || forkAgent == "" {
		t.Fatalf("fork refs workspace=%s private=%s agent=%s", forkW, forkPrivate, forkAgent)
	}
	// A fresh destination avoids unrelated preexisting global uniqueness fixtures;
	// all conversation FKs still must point exclusively at the remapped entities.
	forkTarget := openMigratedDB(t)
	if err = backup.RestoreDump(ctx, forkTarget, dump); err != nil {
		t.Fatal(err)
	}
	var forkSubscriptionWorkspace string
	if err = forkTarget.QueryRow(`SELECT c.workspace_id FROM workspace_conversation_activity a JOIN workspace_conversations c ON c.id=a.conversation_id`).Scan(&forkSubscriptionWorkspace); err != nil || forkSubscriptionWorkspace != forkW {
		t.Fatalf("fork activity reference %q %v", forkSubscriptionWorkspace, err)
	}
	var actualW string
	if err = forkTarget.QueryRow(`SELECT workspace_id FROM workspace_conversations WHERE id=?`, forkPrivate).Scan(&actualW); err != nil || actualW != forkW {
		t.Fatalf("fork scope %q %v", actualW, err)
	}
	var actorCount int
	if err = forkTarget.QueryRow(`SELECT COUNT(*) FROM users WHERE id IN ('chat-owner','chat-peer')`).Scan(&actorCount); err != nil || actorCount != 2 {
		t.Fatalf("conversation-only humans lost: %d %v", actorCount, err)
	}
}
