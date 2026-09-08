package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/groupchatnotify"
)

func TestDirectConversationForkReconcilesCanonicalPair(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	for _, user := range []string{"a-direct-source", "z-direct-source"} {
		if _, err := source.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.invalid", user); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id) VALUES(?,?,?)`, "wm-"+user, workspace, user); err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(source)
	direct, _, err := store.OpenDirect(ctx, workspace, "a-direct-source", "z-direct-source")
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(ctx, workspace, "a-direct-source", direct.ID, groupchat.SendInput{ClientID: "original", Content: "Keep direct history private"})
	if err != nil {
		t.Fatal(err)
	}
	if err = groupchatnotify.New(source, nil, nil).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.SetMuted(ctx, workspace, "z-direct-source", direct.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkRead(ctx, workspace, "z-direct-source", direct.ID, 1); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["workspace_conversation_direct_pairs"]) != 1 {
		t.Fatal("direct pair omitted from backup")
	}
	target := openMigratedDB(t)
	// The destination already knows the same humans by email, under IDs whose
	// lexical order is the reverse of their source IDs. Real restore reconciles
	// these references after RemapIDs; the pair CHECK must still hold.
	for _, identity := range [][2]string{{"z-direct-target", "a-direct-source"}, {"a-direct-target", "z-direct-source"}} {
		if _, err = target.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, identity[0], identity[1]+"@example.invalid", identity[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err = backup.RemapIDs(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	forkWorkspace := dump.Tables["workspaces"][0]["id"].(string)
	forkConversation := dump.Tables["workspace_conversations"][0]["id"].(string)
	if forkWorkspace == workspace || forkConversation == direct.ID {
		t.Fatal("fork IDs did not change")
	}
	_, err = backup.RestoreDumpTxHooks(ctx, target, dump, &backup.RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
		_, err := backup.ReconcileUsersByEmail(ctx, tx, dump)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	var low, high, pairConversation string
	if err = target.QueryRow(`SELECT user_low_id,user_high_id,conversation_id FROM workspace_conversation_direct_pairs WHERE workspace_id=?`, forkWorkspace).Scan(&low, &high, &pairConversation); err != nil {
		t.Fatal(err)
	}
	if low != "a-direct-target" || high != "z-direct-target" || pairConversation != forkConversation {
		t.Fatalf("bad reconciled pair %s %s %s", low, high, pairConversation)
	}
	restored := groupchat.New(target)
	reopened, created, err := restored.OpenDirect(ctx, forkWorkspace, "a-direct-target", "z-direct-target")
	if err != nil || created || reopened.ID != forkConversation || !reopened.IsDirect || !reopened.Muted || reopened.LastReadSequence != 1 {
		t.Fatalf("reopened %+v created%v err%v", reopened, created, err)
	}
	history, err := restored.Messages(ctx, forkWorkspace, "a-direct-target", forkConversation, 0, 10)
	if err != nil || len(history) != 1 || history[0].Content != message.Content || history[0].AuthorUserID != "z-direct-target" {
		t.Fatalf("history %+v %v", history, err)
	}
	if err = restored.SetMuted(ctx, forkWorkspace, "a-direct-target", forkConversation, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restored.Send(ctx, forkWorkspace, "z-direct-target", forkConversation, groupchat.SendInput{ClientID: "after-identity-restore", Content: "One inbox aggregate after restore"}); err != nil {
		t.Fatal(err)
	}
	if err = groupchatnotify.New(target, nil, nil).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var inboxCount int
	if err = target.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE target_user_id='a-direct-target' AND json_extract(payload_json,'$.conversation_id')=?`, forkConversation).Scan(&inboxCount); err != nil || inboxCount != 1 {
		t.Fatalf("reconciled recipient got%d inbox aggregates: %v", inboxCount, err)
	}
	if err = restored.MarkRead(ctx, forkWorkspace, "a-direct-target", forkConversation, 2); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE target_user_id='a-direct-target' AND state='unread' AND json_extract(payload_json,'$.conversation_id')=?`, forkConversation).Scan(&inboxCount); err != nil || inboxCount != 0 {
		t.Fatalf("reconciled recipient unread survived markread: %d %v", inboxCount, err)
	}
	if err = restored.AddMember(ctx, forkWorkspace, "z-direct-target", forkConversation, "u_admin"); !errors.Is(err, groupchat.ErrInvalid) {
		t.Fatalf("restored direct mutable: %v", err)
	}
}

func TestDeletedDirectPeerBackupKeepsFixedConversation(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	for _, user := range []string{"direct-survivor", "direct-deleted"} {
		if _, err := source.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id) VALUES(?,?,?)`, "wm-"+user, workspace, user); err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(source)
	direct, _, err := store.OpenDirect(ctx, workspace, "direct-survivor", "direct-deleted")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Send(ctx, workspace, "direct-survivor", direct.ID, groupchat.SendInput{ClientID: "history", Content: "Surviving history"}); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`DELETE FROM users WHERE id='direct-deleted'`); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["workspace_conversation_direct_pairs"]) != 0 {
		t.Fatal("deleted peer pair retained")
	}
	target := openMigratedDB(t)
	if err = backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	restored := groupchat.New(target)
	c, err := restored.Get(ctx, workspace, "direct-survivor", direct.ID)
	if err != nil || !c.IsDirect {
		t.Fatalf("direct type lost %+v %v", c, err)
	}
	if err = restored.AddMember(ctx, workspace, "direct-survivor", direct.ID, "u_admin"); !errors.Is(err, groupchat.ErrInvalid) {
		t.Fatalf("deleted-peer restored direct mutable: %v", err)
	}
	history, err := restored.Messages(ctx, workspace, "direct-survivor", direct.ID, 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("history %+v %v", history, err)
	}
}
