package backup_test

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesCommentsRestoreHistoryValidationAndProjectionRebuild(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	deck, err := seedBackupSlides(t, source, ws, "comment-slides", "u_admin")
	if err != nil {
		t.Fatal(err)
	}
	store := slides.New(source, slides.Options{})
	key := slides.Key{WorkspaceID: ws, PageID: deck.DeckID}
	if _, err = store.Restore(t.Context(), key, slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "restore_for_anchor", IfOrderVersion: 1, Revision: 1}, backupSlideAuthority); err != nil {
		t.Fatal(err)
	}
	anchor := slides.CommentAnchor{AnchorSchemaVersion: 1, Kind: "element", SlideID: "slide_one", ElementID: "deleted_element", Revision: 3}
	create := slides.CreateCommentRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "comment_original", Anchor: anchor, Body: "Retained Czech: žluťoučký\nsecond line"}
	if _, err = store.CreateComment(t.Context(), key, create, backupSlideAuthority); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplyComment(t.Context(), key, create.MutationID, slides.ReplyCommentRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "reply_original", IfVersion: 1, Body: "Reply"}, backupSlideAuthority); err != nil {
		t.Fatal(err)
	}
	hash, _ := slides.ElementHash(deck.Slides[0].Elements[0])
	if _, err = store.Patch(t.Context(), key, slides.PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "delete_again", Operations: []slides.Operation{{Op: "delete_element", SlideID: anchor.SlideID, ElementID: anchor.ElementID, IfHash: hash}}}, backupSlideAuthority); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Restore(t.Context(), key, slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 4, MutationID: "restore_original_identity", IfOrderVersion: 1, Revision: 3}, backupSlideAuthority); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*backup.DBDump)
		valid  bool
	}{
		{"valid", func(*backup.DBDump) {}, true},
		{"projection_rebuilt", func(d *backup.DBDump) {
			r := d.Tables["slides_comment_threads"][0]
			r["status"] = "detached"
			r["projection_revision"] = 1
			r["last_missing_revision"] = nil
			r["restored_at_revision"] = nil
		}, true},
		{"renamed_thread_without_receipts", func(d *backup.DBDump) {
			d.Tables["slides_comment_threads"][0]["id"] = "renamed_thread"
			for _, r := range d.Tables["slides_comment_events"] {
				r["thread_id"] = "renamed_thread"
			}
			d.Tables["slides_comment_receipts"] = nil
		}, false},
		{"bad_count", func(d *backup.DBDump) { d.Tables["slides_comment_threads"][0]["comment_count"] = 1 }, false},
		{"bad_version", func(d *backup.DBDump) { d.Tables["slides_comment_threads"][0]["version"] = 3 }, false},
		{"missing_original_anchor", func(d *backup.DBDump) { d.Tables["slides_comment_threads"][0]["element_id"] = "never_existed" }, false},
		{"absent_at_pinned_revision", func(d *backup.DBDump) { d.Tables["slides_comment_threads"][0]["anchor_revision"] = 2 }, false},
		{"bad_kind_sequence", func(d *backup.DBDump) {
			d.Tables["slides_comment_events"][1]["kind"] = "reopen"
			d.Tables["slides_comment_events"][1]["body"] = ""
		}, false},
		{"receipt_hash_mismatch", func(d *backup.DBDump) { d.Tables["slides_comment_receipts"][0]["request_hash"] = "invalid" }, false},
		{"receipt_actor_mismatch", func(d *backup.DBDump) { d.Tables["slides_comment_receipts"][0]["actor"] = "other" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dump, e := backup.DumpWorkspace(t.Context(), source, ws)
			if e != nil {
				t.Fatal(e)
			}
			tc.mutate(dump)
			target := openMigratedDB(t)
			e = backup.RestoreDump(t.Context(), target, dump)
			if !tc.valid {
				if e == nil {
					t.Fatal("malformed restore accepted")
				}
				var n int
				if e = target.QueryRow(`SELECT COUNT(*) FROM slides_heads`).Scan(&n); e != nil || n != 0 {
					t.Fatal("restore did not rollback", n, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			restored := slides.New(target, slides.Options{})
			thread, e := restored.CommentThread(t.Context(), key, create.MutationID, backupSlideAuthority)
			if e != nil || thread.Status != "restored" || thread.ProjectionRevision != 5 || thread.LastMissingRevision == nil || *thread.LastMissingRevision != 4 || thread.RestoredAtRevision == nil || *thread.RestoredAtRevision != 5 || thread.CommentCount != 2 || thread.Version != 2 {
				t.Fatal(thread, e)
			}
			receipt, e := restored.CreateComment(t.Context(), key, create, backupSlideAuthority)
			if e != nil || !receipt.Duplicate {
				t.Fatal(receipt, e)
			}
		})
	}
	// Actual encrypted workspace fork exercises remapping and clears only replay
	// receipts. Historical event mutation IDs remain non-replayable tombstones.
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "comments-fork-secret", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	if _, err = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: bundle.Path, Passphrase: "comments-fork-secret", AsWorkspace: "comments-fork", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	var forkKey slides.Key
	var threadID string
	if err = target.QueryRow(`SELECT workspace_id,page_id,id FROM slides_comment_threads`).Scan(&forkKey.WorkspaceID, &forkKey.PageID, &threadID); err != nil {
		t.Fatal(err)
	}
	forkStore := slides.New(target, slides.Options{})
	thread, err := forkStore.CommentThread(t.Context(), forkKey, threadID, backupSlideAuthority)
	if err != nil || thread.ID != create.MutationID || thread.Anchor != anchor || thread.Status != "restored" || forkKey.PageID == key.PageID {
		t.Fatal(thread, err)
	}
	var count int
	if err = target.QueryRow(`SELECT COUNT(*) FROM slides_comment_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	create.PageID = forkKey.PageID
	if _, err = forkStore.CreateComment(t.Context(), forkKey, create, backupSlideAuthority); !errors.Is(err, slides.ErrDuplicate) {
		t.Fatal("fork replay did not hit historical tombstone", err)
	}
}
