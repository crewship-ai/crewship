package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/inbox"
	_ "modernc.org/sqlite"
)

var notifyITCounter atomic.Int64

// newNotifyIntegrationDB brings up a fully-migrated in-memory DB so the
// notify step's two DB-backed collaborators (the inbox sink + the
// membership checker) can be exercised against the real schema, not a fake.
func newNotifyIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("crewship-notify-it-%d", notifyITCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestSQLInboxNotifier_InsertsAndDedupes proves the production sink writes
// a real inbox_items row and that the (kind, source_id) index makes a
// retry idempotent — the run:step SourceID contract the notify step relies
// on end-to-end.
func TestSQLInboxNotifier_InsertsAndDedupes(t *testing.T) {
	db := newNotifyIntegrationDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','ws','ws')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	n := &sqlInboxNotifier{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	item := inbox.Item{
		WorkspaceID: "ws1",
		Kind:        inbox.KindMessage,
		SourceID:    "run_1:tell",
		Title:       "Invoices parsed",
		BodyMD:      "Parsed 3 invoices.",
		SenderType:  "pipeline",
		SenderName:  "invoice-bot",
		Payload:     map[string]interface{}{"subkind": "routine_update"},
	}
	if err := n.Notify(context.Background(), item); err != nil {
		t.Fatalf("notify: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message' AND source_id='run_1:tell'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want 1 inbox row after notify, got %d", count)
	}
	// Retry with the same SourceID must not double-post.
	if err := n.Notify(context.Background(), item); err != nil {
		t.Fatalf("notify retry: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='run_1:tell'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("retry double-inserted: got %d rows, want 1 (idempotent)", count)
	}
}

// TestWorkspaceMemberChecker_RealDB exercises the membership predicate the
// notify step uses to avoid black-holing a user: target.
func TestWorkspaceMemberChecker_RealDB(t *testing.T) {
	db := newNotifyIntegrationDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','ws','ws')`)
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u_member','m@example.com')`)
	mustExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm1','ws1','u_member','MEMBER')`)

	check := NewWorkspaceMemberChecker(db)

	if ok, err := check(context.Background(), "ws1", "u_member"); err != nil || !ok {
		t.Errorf("member: got (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := check(context.Background(), "ws1", "u_ghost"); err != nil || ok {
		t.Errorf("non-member: got (%v, %v), want (false, nil)", ok, err)
	}
	// Empty ids short-circuit to "not a member" without a query.
	if ok, _ := check(context.Background(), "", "u_member"); ok {
		t.Error("empty workspace id must report non-member")
	}
	if ok, _ := check(context.Background(), "ws1", ""); ok {
		t.Error("empty user id must report non-member")
	}
}

// TestRunNoticeCounter_RealDB exercises the production per-recipient cap
// counter against the real inbox_items schema: it must count only THIS
// run's routine notices to the SAME recipient — matching NULL targets for
// a workspace notice and an exact id for a user notice — so the soft cap
// counts the right rows.
func TestRunNoticeCounter_RealDB(t *testing.T) {
	db := newNotifyIntegrationDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','ws','ws')`)
	n := &sqlInboxNotifier{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Two notices to u_bob and one to workspace (NULL target), all in run_1;
	// plus one notice in a different run that must NOT be counted.
	insert := func(source, user string) {
		if err := n.Notify(context.Background(), inbox.Item{
			WorkspaceID: "ws1", Kind: inbox.KindMessage, SourceID: source,
			TargetUserID: user, Title: "t", SenderType: "pipeline",
			Payload: map[string]interface{}{"subkind": "routine_update"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("run_1:s1", "u_bob")
	insert("run_1:s2", "u_bob")
	insert("run_1:s3", "")      // workspace notice (NULL target)
	insert("run_2:s1", "u_bob") // different run — excluded

	count := NewRunNoticeCounter(db)
	ctx := context.Background()

	if got, err := count(ctx, "ws1", "run_1", "u_bob", ""); err != nil || got != 2 {
		t.Errorf("u_bob in run_1: got (%d,%v), want (2,nil)", got, err)
	}
	if got, err := count(ctx, "ws1", "run_1", "", ""); err != nil || got != 1 {
		t.Errorf("workspace notice in run_1: got (%d,%v), want (1,nil)", got, err)
	}
	if got, err := count(ctx, "ws1", "run_1", "u_other", ""); err != nil || got != 0 {
		t.Errorf("unrelated recipient: got (%d,%v), want (0,nil)", got, err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
