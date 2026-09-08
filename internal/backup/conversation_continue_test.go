package backup_test

import (
	"context"
	"database/sql"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"testing"
)

func TestContinuationForkAndEmailReconcileKeepRetryIdentity(t *testing.T) {
	ctx := t.Context()
	source := openMigratedDB(t)
	w := seedWorkspace(t, source)
	for _, id := range []string{"continue-one", "continue-two", "continue-three"} {
		if _, err := source.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, id, id+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id) VALUES(?,?,?)`, "wm-"+id, w, id); err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(source)
	dm, _, err := store.OpenDirect(ctx, w, "continue-one", "continue-two")
	if err != nil {
		t.Fatal(err)
	}
	input := groupchat.ContinueInput{Kind: "group", Title: "Fresh planning", MemberIDs: []string{"continue-three"}, ClientID: "stable-retry"}
	group, _, err := store.Continue(ctx, w, "continue-one", dm.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["workspace_conversation_continuations"]) != 1 {
		t.Fatal("retry map missing from backup")
	}
	target := openMigratedDB(t)
	for _, id := range []string{"continue-one", "continue-two", "continue-three"} {
		if _, err = target.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, "target-"+id, id+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err = backup.RemapIDs(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	forkW := dump.Tables["workspaces"][0]["id"].(string)
	forkDM := dump.Tables["workspace_conversation_direct_pairs"][0]["conversation_id"].(string)
	forkGroup := dump.Tables["workspace_conversation_continuations"][0]["target_conversation_id"].(string)
	if forkDM == dm.ID || forkGroup == group.ID {
		t.Fatal("room references not remapped")
	}
	if _, err = backup.RestoreDumpTxHooks(ctx, target, dump, &backup.RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
		_, err := backup.ReconcileUsersByEmail(ctx, tx, dump)
		return err
	}}); err != nil {
		t.Fatal(err)
	}
	input.MemberIDs = []string{"target-continue-three"}
	restored, created, err := groupchat.New(target).Continue(ctx, forkW, "target-continue-one", forkDM, input)
	if err != nil || created || restored.ID != forkGroup {
		t.Fatalf("retry lost after fork/reconcile: %#v created=%v err=%v", restored, created, err)
	}
}
