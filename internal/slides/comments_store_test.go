package slides

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func commentAnchorFixture(t *testing.T, deck Deck, revision int64) CommentAnchor {
	t.Helper()
	for _, slide := range deck.Slides {
		for _, element := range slide.Elements {
			if element.Type == "text" {
				return CommentAnchor{AnchorSchemaVersion: 1, Kind: "element", SlideID: slide.ID, ElementID: element.ID, Revision: revision}
			}
		}
	}
	t.Fatal("text fixture missing")
	return CommentAnchor{}
}
func createCommentFixture(t *testing.T, store *Store, key Key, anchor CommentAnchor, id string) CommentReceipt {
	t.Helper()
	receipt, err := store.CreateComment(t.Context(), key, CreateCommentRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: anchor.Revision, MutationID: id, Anchor: anchor, Body: "Český komentář\nRetained"}, storeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func TestCommentsAtomicHistoryPaginationAndReplay(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	anchor := commentAnchorFixture(t, deck, 1)
	created := createCommentFixture(t, store, key, anchor, "thread_one")
	if created.ThreadID != "thread_one" || created.Version != 1 {
		t.Fatal(created)
	}
	reply := ReplyCommentRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "reply_one", IfVersion: 1, Body: "Reply"}
	ack, err := store.ReplyComment(t.Context(), key, created.ThreadID, reply, storeAuthority)
	if err != nil || ack.Version != 2 {
		t.Fatal(ack, err)
	}
	duplicate, err := store.ReplyComment(t.Context(), key, created.ThreadID, reply, storeAuthority)
	if err != nil || !duplicate.Duplicate {
		t.Fatal(duplicate, err)
	}
	changed := reply
	changed.Body = "other"
	if _, err = store.ReplyComment(t.Context(), key, created.ThreadID, changed, storeAuthority); !errors.Is(err, ErrDuplicate) {
		t.Fatal("changed receipt accepted", err)
	}
	changed = reply
	changed.MutationID = "stale_reply"
	if _, err = store.ReplyComment(t.Context(), key, created.ThreadID, changed, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	resolve := ResolveCommentRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "resolve_one", IfVersion: 2, Resolved: true}
	if _, err = store.ResolveComment(t.Context(), key, created.ThreadID, resolve, storeAuthority); err != nil {
		t.Fatal(err)
	}
	changed.IfVersion = 3
	if _, err = store.ReplyComment(t.Context(), key, created.ThreadID, changed, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("commenter reopened closed thread", err)
	}
	resolve.MutationID = "reopen_one"
	resolve.IfVersion = 3
	resolve.Resolved = false
	if _, err = store.ResolveComment(t.Context(), key, created.ThreadID, resolve, storeAuthority); err != nil {
		t.Fatal(err)
	}
	changed.MutationID = "reply_two"
	changed.IfVersion = 4
	if _, err = store.ReplyComment(t.Context(), key, created.ThreadID, changed, storeAuthority); err != nil {
		t.Fatal(err)
	}
	history, err := store.CommentHistory(t.Context(), key, created.ThreadID, CommentPageRequest{Limit: 2}, storeAuthority)
	if err != nil || history.Watermark != 5 || !history.HasMore || len(history.Items) != 2 {
		t.Fatal(history, err)
	}
	changed.MutationID = "reply_after_watermark"
	changed.IfVersion = 5
	if _, err = store.ReplyComment(t.Context(), key, created.ThreadID, changed, storeAuthority); err != nil {
		t.Fatal(err)
	}
	versions := []int64{1, 2}
	cursor := history.NextCursor
	for cursor != "" {
		history, err = store.CommentHistory(t.Context(), key, created.ThreadID, CommentPageRequest{Limit: 2, Cursor: cursor}, storeAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if history.Watermark != 5 {
			t.Fatal("history watermark widened")
		}
		for _, event := range history.Items {
			versions = append(versions, event.Version)
		}
		cursor = history.NextCursor
	}
	if fmt.Sprint(versions) != "[1 2 3 4 5]" {
		t.Fatal("history omitted/duplicated events", versions)
	}
	for _, id := range []string{"thread_two", "thread_three"} {
		createCommentFixture(t, store, key, anchor, id)
	}
	page, err := store.CommentThreads(t.Context(), key, CommentPageRequest{Limit: 1}, storeAuthority)
	if err != nil || page.Watermark != 3 || !page.HasMore {
		t.Fatal(page, err)
	}
	createCommentFixture(t, store, key, anchor, "thread_after_watermark")
	ids := []string{page.Items[0].ID}
	cursor = page.NextCursor
	for cursor != "" {
		page, err = store.CommentThreads(t.Context(), key, CommentPageRequest{Limit: 1, Cursor: cursor}, storeAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if page.Watermark != 3 {
			t.Fatal("thread watermark widened")
		}
		for _, thread := range page.Items {
			ids = append(ids, thread.ID)
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(ids) != "[thread_one thread_two thread_three]" {
		t.Fatal("threads omitted/duplicated", ids)
	}
	latest, err := store.CommentThreads(t.Context(), key, CommentPageRequest{Limit: 100}, storeAuthority)
	if err != nil || len(latest.Items) != 4 {
		t.Fatal(latest, err)
	}
	foreignCursor := commentCursor{CursorSchemaVersion: 1, WorkspaceID: key.WorkspaceID, PageID: "replacement_page", After: 1, Watermark: 3}
	if _, err = store.CommentThreads(t.Context(), key, CommentPageRequest{Cursor: encodeCommentCursor(foreignCursor)}, storeAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor lost immutable namespace", err)
	}
	if _, err = store.CommentHistory(t.Context(), key, "thread_two", CommentPageRequest{Cursor: encodeCommentCursor(commentCursor{CursorSchemaVersion: 1, WorkspaceID: key.WorkspaceID, PageID: key.PageID, ThreadID: "thread_one", After: 1, Watermark: 5})}, storeAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-thread cursor accepted", err)
	}
	raw := `{"cursor_schema_version":1,"workspace_id":"` + key.WorkspaceID + `","page_id":"` + key.PageID + `","after":1,"AFTER":2,"watermark":3}`
	if _, err = store.CommentThreads(t.Context(), key, CommentPageRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(raw))}, storeAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatal("case alias cursor accepted", err)
	}
	if currentState(t, store, key).Revision != 1 {
		t.Fatal("comments changed deck revision")
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM slides_comment_events WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count); err != nil || count != 9 {
		t.Fatal(count, err)
	}
}
func TestCommentsAnchorProjectionRestorePinAndPageCascade(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	anchor := commentAnchorFixture(t, deck, 1)
	createCommentFixture(t, store, key, anchor, "thread_one")
	state := currentState(t, store, key)
	patch := textPatch(t, state, "human_text_change", "Changed human text")
	if _, err := store.Patch(t.Context(), key, patch, storeAuthority); err != nil {
		t.Fatal(err)
	}
	state = currentState(t, store, key)
	order := append([]string{}, state.Deck.SlideOrder...)
	order[0], order[1] = order[1], order[0]
	if _, err := store.Reorder(t.Context(), key, ReorderRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "human_reorder", IfOrderVersion: 1, SlideOrder: order}, storeAuthority); err != nil {
		t.Fatal(err)
	}
	thread, err := store.CommentThread(t.Context(), key, "thread_one", storeAuthority)
	if err != nil || thread.Status != "attached" || thread.ProjectionRevision != 3 || thread.Version != 1 || thread.Anchor != anchor {
		t.Fatal(thread, err)
	}
	state = currentState(t, store, key)
	var element Element
	for _, slide := range state.Deck.Slides {
		for _, candidate := range slide.Elements {
			if candidate.ID == anchor.ElementID {
				element = candidate
			}
		}
	}
	hash, _ := ElementHash(element)
	if _, err := store.Patch(t.Context(), key, PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "delete_target", Operations: []Operation{{Op: "delete_element", SlideID: anchor.SlideID, ElementID: anchor.ElementID, IfHash: hash}}}, storeAuthority); err != nil {
		t.Fatal(err)
	}
	thread, err = store.CommentThread(t.Context(), key, "thread_one", storeAuthority)
	if err != nil || thread.Status != "detached" || thread.LastMissingRevision == nil || *thread.LastMissingRevision != 4 || thread.Version != 1 {
		t.Fatal(thread, err)
	}
	if _, err = store.ReplyComment(t.Context(), key, "thread_one", ReplyCommentRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "detached_reply", IfVersion: 1, Body: "Discuss retained target"}, storeAuthority); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 4, MutationID: "restore_original", IfOrderVersion: 2, Revision: 1}, storeAuthority); err != nil {
		t.Fatal(err)
	}
	thread, err = store.CommentThread(t.Context(), key, "thread_one", storeAuthority)
	if err != nil || thread.Status != "restored" || thread.RestoredAtRevision == nil || *thread.RestoredAtRevision != 5 || thread.Version != 2 || thread.ProjectionRevision != 5 || thread.Anchor != anchor {
		t.Fatal(thread, err)
	}
	for _, query := range []string{`UPDATE slides_comment_threads SET slide_id='similar'`, `UPDATE slides_comment_events SET body='overwrite'`, `DELETE FROM slides_comment_events`, `DELETE FROM slides_comment_threads`} {
		if _, err = db.Exec(query); err == nil {
			t.Fatalf("mutable retained comment contract: %s", query)
		}
	}
	// NO ACTION is deferred so an intentional Page-owned cascade can delete its
	// entire graph, but standalone retained revision GC fails at COMMIT.
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `DELETE FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=1`, key.WorkspaceID, key.PageID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err == nil {
		t.Fatal("retained comment failed to pin anchor revision")
	}
	_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
	conn.Close()
	if _, err = store.Read(t.Context(), key, 1, storeAuthority); err != nil {
		t.Fatal("failed revision GC lost retained snapshot", err)
	}
	if _, err = db.Exec(`DELETE FROM pages WHERE id=?`, key.PageID); err != nil {
		t.Fatal("Page cascade blocked by retained history triggers", err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_comment_threads", "slides_comment_events", "slides_comment_receipts"} {
		assertRows(t, db, table, 0)
	}
}
func TestCommentsHeadCascadeAndFinalAuthorityRollback(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	anchor := commentAnchorFixture(t, deck, 1)
	calls := 0
	authority := func(ctx context.Context, tx *sql.Tx) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("revoked before commit")
		}
		return storeAuthority(ctx, tx)
	}
	if _, err := store.CreateComment(t.Context(), key, CreateCommentRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "revoked_create", Anchor: anchor, Body: "Draft"}, authority); err == nil {
		t.Fatal("revoked comment committed")
	}
	for _, table := range []string{"slides_comment_threads", "slides_comment_events", "slides_comment_receipts"} {
		assertRows(t, db, table, 0)
	}
	createCommentFixture(t, store, key, anchor, "thread_one")
	denied := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("revoked") }
	if _, err := store.CreateComment(t.Context(), key, CreateCommentRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "thread_one", Anchor: anchor, Body: "Český komentář\nRetained"}, denied); err == nil {
		t.Fatal("receipt replay bypassed live authority")
	}
	if _, err := db.Exec(`DELETE FROM slides_heads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID); err != nil {
		t.Fatal("head cascade failed", err)
	}
	for _, table := range []string{"slides_comment_threads", "slides_comment_events", "slides_comment_receipts"} {
		assertRows(t, db, table, 0)
	}
	var pages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id=?`, key.PageID).Scan(&pages); err != nil || pages != 1 {
		t.Fatal("head deletion erased parent Page", err)
	}
}

func TestCommentsTypedValidationPreservesValidWireTextBudget(t *testing.T) {
	store, _, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	anchor := commentAnchorFixture(t, deck, 1)
	for _, test := range []struct{ name, body string }{
		{"less_than", strings.Repeat("<", MaxCommentBodyBytes)},
		{"ampersand", strings.Repeat("&", MaxCommentBodyBytes)},
		{"line_separator", "x" + strings.Repeat("\u2028", 3333)},
		{"paragraph_separator", "x" + strings.Repeat("\u2029", 3333)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := CreateCommentRequest{PageID: key.PageID, SchemaVersion: CommentProtocolVersion, BaseRev: 1, MutationID: "wire_" + test.name, Anchor: anchor, Body: "PLACEHOLDER"}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			// These controlled values contain no JSON quotes/backslashes/controls. Actual
			// Unicode separators are legal wire JSON and cannot be replaced by escapes.
			wire := strings.Replace(string(encoded), `"PLACEHOLDER"`, `"`+test.body+`"`, 1)
			if len(wire) > MaxCommentRequestBytes {
				t.Fatal("fixture exceeds wire budget", len(wire))
			}
			request, err = DecodeCreateComment([]byte(wire))
			if err != nil {
				t.Fatal("valid original wire rejected", err)
			}
			if _, err = store.CreateComment(t.Context(), key, request, storeAuthority); err != nil {
				t.Fatal("typed reserialization rejected valid wire", err)
			}
			reply := ReplyCommentRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "reply_" + test.name, IfVersion: 1, Body: test.body}
			if _, err = store.ReplyComment(t.Context(), key, request.MutationID, reply, storeAuthority); err != nil {
				t.Fatal("typed reply rejected valid field budget", err)
			}
			history, err := store.CommentHistory(t.Context(), key, request.MutationID, CommentPageRequest{}, storeAuthority)
			if err != nil || len(history.Items) != 2 || history.Items[0].Body != test.body || history.Items[1].Body != test.body {
				t.Fatal("text normalized or lost", err)
			}
		})
	}
	// Typed field admission never weakens the original HTTP wire limit: escaped
	// LF must still fit16KiB before the server reaches any typed Store call.
	oversized := CreateCommentRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "oversized_wire", Anchor: anchor, Body: "x" + strings.Repeat("\n", MaxCommentBodyBytes-1)}
	raw, _ := json.Marshal(oversized)
	if len(raw) <= MaxCommentRequestBytes {
		t.Fatal("fixture must exceed wire limit")
	}
	if _, err := DecodeCreateComment(raw); err == nil {
		t.Fatal("escaped oversized wire bypassed16KiB")
	}
}
