package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDocumentCommentsThreadsRepliesAndResolve(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	anchor := json.RawMessage(`{"id":"root","block":"one","quote":"Bloky"}`)
	generation := int64(0)
	root, e := s.Comment(ctx, key, CommentInput{ID: "root", Fingerprint: "f-root", Body: "First", Anchor: anchor, IfRev: 0, Generation: &generation}, allow)
	if e != nil || root.Kind != "comment" || root.Rev != 0 {
		t.Fatalf("root: %+v %v", root, e)
	}
	if _, e = s.Comment(ctx, key, CommentInput{ID: "reply", Fingerprint: "f-reply", Parent: "root", Body: "Answer", IfRev: 0, Generation: &generation}, allow); e != nil {
		t.Fatal(e)
	}
	list, e := s.Comments(ctx, key, allow)
	if e != nil || len(list) != 2 || list[0].ID != "root" || string(list[0].Anchor) != string(anchor) || list[1].Parent != "root" || list[1].Anchor != nil || list[0].Actor != "server-actor" {
		t.Fatalf("list: %+v %v", list, e)
	}
	// Comments leave content, revision, epoch and generation alone.
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 0 || v.Generation != 0 || string(v.Snapshot) != "baseline" {
		t.Fatalf("comment changed document: %+v %v", v, e)
	}
	again, e := s.Comment(ctx, key, CommentInput{ID: "root", Fingerprint: "f-root", Body: "First", Anchor: anchor, IfRev: 0, Generation: &generation}, allow)
	if e != nil || again != root {
		t.Fatalf("exact retry: %+v %v", again, e)
	}
	if _, e = s.Comment(ctx, key, CommentInput{ID: "root", Fingerprint: "f-root", Body: "Changed", Anchor: anchor, IfRev: 0, Generation: &generation}, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed retry: %v", e)
	}
	other := func(context.Context, *sql.Tx) (string, error) { return "other-actor", nil }
	if _, e = s.Comment(ctx, key, CommentInput{ID: "root", Fingerprint: "f-root", Body: "First", Anchor: anchor, IfRev: 0, Generation: &generation}, other); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("other actor replayed receipt: %v", e)
	}
	if _, e = s.ResolveComment(ctx, key, "resolve-reply", "f", "reply", 1, true, 0, &generation, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("reply resolved: %v", e)
	}
	r, e := s.ResolveComment(ctx, key, "resolve-1", "f", "root", 1, true, 0, &generation, allow)
	if e != nil || r.Kind != "resolve" {
		t.Fatalf("resolve: %+v %v", r, e)
	}
	if _, e = s.ResolveComment(ctx, key, "resolve-2", "f", "root", 1, false, 0, &generation, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale version reopened: %v", e)
	}
	list, _ = s.Comments(ctx, key, allow)
	if !list[0].Resolved || list[0].Version != 2 {
		t.Fatalf("resolved state: %+v", list[0])
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM document_receipts WHERE kind IN ('comment','resolve')`).Scan(&count); e != nil || count != 3 {
		t.Fatalf("receipts: %d %v", count, e)
	}
}

func TestDocumentCommentsRefuseInvalidStaleAndUnauthorized(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	anchor := json.RawMessage(`{"id":"x"}`)
	zero, stale := int64(0), int64(7)
	cases := map[string]struct {
		in   CommentInput
		want error
	}{
		"empty body":       {CommentInput{ID: "a", Body: "", Anchor: anchor, Generation: &zero}, ErrInvalidComment},
		"long body":        {CommentInput{ID: "a", Body: strings.Repeat("ž", CommentBodyLimit+1), Anchor: anchor, Generation: &zero}, ErrInvalidComment},
		"root no anchor":   {CommentInput{ID: "a", Body: "x", Generation: &zero}, ErrInvalidComment},
		"reply anchor":     {CommentInput{ID: "a", Body: "x", Parent: "p", Anchor: anchor, Generation: &zero}, ErrInvalidComment},
		"invalid anchor":   {CommentInput{ID: "a", Body: "x", Anchor: json.RawMessage(`{`), Generation: &zero}, ErrInvalidComment},
		"no generation":    {CommentInput{ID: "a", Body: "x", Anchor: anchor}, ErrInvalidComment},
		"stale revision":   {CommentInput{ID: "a", Body: "x", Anchor: anchor, IfRev: 3, Generation: &zero}, ErrConflict},
		"stale generation": {CommentInput{ID: "a", Body: "x", Anchor: anchor, Generation: &stale}, ErrConflict},
		"missing parent":   {CommentInput{ID: "a", Body: "x", Parent: "missing", Generation: &zero}, ErrNotFound},
	}
	for name, tc := range cases {
		if _, e := s.Comment(ctx, key, tc.in, allow); !errors.Is(e, tc.want) {
			t.Fatalf("%s: %v", name, e)
		}
	}
	denied := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("denied") }
	if _, e := s.Comment(ctx, key, CommentInput{ID: "a", Body: "x", Anchor: anchor, Generation: &zero}, denied); e == nil {
		t.Fatal("unauthorized comment admitted")
	}
	if _, e := s.Comments(ctx, key, denied); e == nil {
		t.Fatal("unauthorized list admitted")
	}
	if _, e := s.Comment(ctx, Key{"workspace-a", "page-c"}, CommentInput{ID: "a", Body: "x", Anchor: anchor, Generation: &zero}, allow); !errors.Is(e, ErrNotFound) {
		t.Fatalf("comment without document: %v", e)
	}
	if _, e := s.Comment(ctx, key, CommentInput{ID: "root", Body: "x", Anchor: anchor, Generation: &zero}, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Comment(ctx, key, CommentInput{ID: "reply", Body: "x", Parent: "root", Generation: &zero}, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Comment(ctx, key, CommentInput{ID: "nested", Body: "x", Parent: "reply", Generation: &zero}, allow); !errors.Is(e, ErrInvalidComment) {
		t.Fatalf("nested reply: %v", e)
	}
	list, e := s.Comments(ctx, key, allow)
	if e != nil || len(list) != 2 {
		t.Fatalf("refused writes left rows: %+v %v", list, e)
	}
}

func TestDocumentCommentsLimitAndPageDeletion(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	zero := int64(0)
	var base int64
	if e := db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='workspace-a'`).Scan(&base); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO document_comments(workspace_id,page_id,id,body,anchor_json,actor,rev) SELECT 'workspace-a','page-a','c'||i,'x','{}','a',0 FROM n`, CommentLimit); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Comment(ctx, key, CommentInput{ID: "over", Body: "x", Anchor: json.RawMessage(`{}`), Generation: &zero}, allow); !errors.Is(e, ErrQuota) {
		t.Fatalf("limit: %v", e)
	}
	// Comment rows are charged to the aggregate artifact storage budget.
	var used int64
	if e := db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='workspace-a'`).Scan(&used); e != nil || used-base < int64(CommentLimit*(256+2)) {
		t.Fatalf("comments not charged: %d -> %d %v", base, used, e)
	}
	// Each thread is resolved or reopened a bounded number of times; another
	// thread is unaffected.
	if _, e := db.Exec(`UPDATE document_comments SET version=? WHERE id='c1'`, ResolveLimit+1); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ResolveComment(ctx, key, "one-more", "f", "c1", ResolveLimit+1, true, 0, &zero, allow); !errors.Is(e, ErrQuota) {
		t.Fatalf("resolve limit: %v", e)
	}
	if _, e := s.ResolveComment(ctx, key, "other-thread", "f", "c2", 1, true, 0, &zero, allow); e != nil {
		t.Fatalf("other thread: %v", e)
	}
	if _, e := db.Exec(`DELETE FROM pages WHERE id='page-a'`); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM document_comments`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("comments survived page deletion: %d %v", n, e)
	}
	var after int64
	if e := db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='workspace-a'`).Scan(&after); e != nil || used-after < int64(CommentLimit*(256+2)) {
		t.Fatalf("comment storage not released: before %d after %d %v", used, after, e)
	}
}

// Discussion and content edits have separate receipt windows: a commenter at
// the limit cannot make an editor's save wait.
func TestDocumentCommentsDoNotShareTheEditWindow(t *testing.T) {
	s, db, _ := fixture(t, Options{MaxCommits: 2})
	zero := int64(0)
	for _, id := range []string{"a", "b"} {
		if _, e := s.Comment(ctx, key, CommentInput{ID: id, Body: "x", Anchor: json.RawMessage(`{}`), Generation: &zero}, allow); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Comment(ctx, key, CommentInput{ID: "c", Body: "x", Anchor: json.RawMessage(`{}`), Generation: &zero}, allow); !errors.Is(e, ErrRateLimit) {
		t.Fatalf("discussion window: %v", e)
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback() }()
	if e = s.admitNewReceipt(ctx, tx, key); e != nil {
		t.Fatalf("edit window consumed by comments: %v", e)
	}
}
