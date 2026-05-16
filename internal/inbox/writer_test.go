package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	_ "modernc.org/sqlite"
)

// inboxTestCounter generates unique in-memory DB names per parallel
// test. Same pattern as internal/database/migrate_v89_test.go — a bare
// `file::memory:?cache=shared` DSN points every connection at the SAME
// global in-memory database and leaks rows between t.Parallel() siblings.
var inboxTestCounter atomic.Int64

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newInboxTestDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("crewship-inbox-test-%d", inboxTestCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// inbox_items.workspace_id has a FK to workspaces — seed a row so
	// Insert calls don't get rejected by referential integrity.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'ws', 'ws')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return db
}

func TestInsert_HappyPath(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID:  "ws1",
		Kind:         "waitpoint",
		SourceID:     "wp-1",
		TargetUserID: "u1",
		Title:        "Approve deploy",
		BodyMD:       "**Deploy to prod?**",
		SenderType:   "agent",
		SenderID:     "a1",
		SenderName:   "Alice",
		Priority:     "high",
		Blocking:     true,
		Payload:      map[string]interface{}{"branch": "main"},
	})

	var (
		id, kind, sourceID, title, bodyMD, state, priority, senderName string
		blocking                                                       int
		payloadJSON                                                    string
	)
	row := db.QueryRow(`
		SELECT id, kind, source_id, title, body_md, state, priority,
		       sender_name, blocking, payload_json
		FROM inbox_items WHERE source_id = 'wp-1'`)
	if err := row.Scan(&id, &kind, &sourceID, &title, &bodyMD, &state, &priority,
		&senderName, &blocking, &payloadJSON); err != nil {
		t.Fatalf("read inserted row: %v", err)
	}
	if id != "ibx_waitpoint_wp-1" {
		t.Errorf("id: want ibx_waitpoint_wp-1, got %q", id)
	}
	if kind != "waitpoint" || sourceID != "wp-1" || title != "Approve deploy" ||
		bodyMD != "**Deploy to prod?**" || state != "unread" || priority != "high" ||
		senderName != "Alice" {
		t.Errorf("scalar columns mismatched: kind=%q source_id=%q title=%q body=%q state=%q priority=%q sender=%q",
			kind, sourceID, title, bodyMD, state, priority, senderName)
	}
	if blocking != 1 {
		t.Errorf("blocking: want 1, got %d", blocking)
	}
	// Payload is marshalled JSON — accept either map order for the
	// single-key case by just substring-matching the value.
	if payloadJSON != `{"branch":"main"}` {
		t.Errorf("payload_json: want {\"branch\":\"main\"}, got %q", payloadJSON)
	}
}

func TestInsert_DedupesOnKindSourceID(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// Same (kind, source_id) inserted twice — the unique index on
	// (kind, source_id) is the dedup key, INSERT OR IGNORE means the
	// second call is a no-op rather than an error.
	for i := 0; i < 2; i++ {
		Insert(ctx, db, quietLogger(), Item{
			WorkspaceID: "ws1",
			Kind:        "escalation",
			SourceID:    "esc-1",
			Title:       fmt.Sprintf("attempt %d", i),
		})
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = 'esc-1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("dedupe failed: want 1 row, got %d", count)
	}

	// First call's title wins (INSERT OR IGNORE keeps the existing row).
	var title string
	if err := db.QueryRow(`SELECT title FROM inbox_items WHERE source_id = 'esc-1'`).Scan(&title); err != nil {
		t.Fatalf("title: %v", err)
	}
	if title != "attempt 0" {
		t.Errorf("first-write-wins violated: got %q", title)
	}
}

func TestInsert_DefaultsPriorityWhenEmpty(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "message",
		SourceID:    "msg-1",
		// Priority deliberately omitted — writer should fill in "medium".
	})

	var priority string
	if err := db.QueryRow(`SELECT priority FROM inbox_items WHERE source_id = 'msg-1'`).Scan(&priority); err != nil {
		t.Fatalf("priority: %v", err)
	}
	if priority != "medium" {
		t.Errorf("default priority: want medium, got %q", priority)
	}
}

func TestInsert_EmptyOptionalFieldsBecomeNULL(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// Only the required fields — TargetUserID/Role and Sender* are
	// omitted. The writer's NULLIF(?, '') wraps mean they should land
	// as SQL NULL, not as empty strings (so the partial unread index
	// in v85 keeps working and the dashboard's "for me" filter doesn't
	// match every row).
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "failed_run",
		SourceID:    "run-1",
		Title:       "Run failed",
	})

	var (
		targetUser, targetRole, senderType, senderID, senderName sql.NullString
	)
	row := db.QueryRow(`
		SELECT target_user_id, target_role, sender_type, sender_id, sender_name
		FROM inbox_items WHERE source_id = 'run-1'`)
	if err := row.Scan(&targetUser, &targetRole, &senderType, &senderID, &senderName); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for name, v := range map[string]sql.NullString{
		"target_user_id": targetUser,
		"target_role":    targetRole,
		"sender_type":    senderType,
		"sender_id":      senderID,
		"sender_name":    senderName,
	} {
		if v.Valid {
			t.Errorf("%s: want NULL, got %q", name, v.String)
		}
	}
}

func TestInsert_NilPayloadStoredAsEmptyObject(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "message",
		SourceID:    "msg-empty",
		Payload:     nil, // explicit nil; writer should default to "{}"
	})

	var payload string
	if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE source_id = 'msg-empty'`).Scan(&payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload != "{}" {
		t.Errorf("nil payload should stringify to {}; got %q", payload)
	}
}

func TestInsert_ValidatesRequiredFields(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// Every shape with a missing required field must be a no-op (no
	// row written, no error returned). The writer's early-return
	// guard exists so callers can pass partial data from upstream
	// parse failures without poisoning the inbox.
	cases := []Item{
		{WorkspaceID: "", Kind: "waitpoint", SourceID: "x"},
		{WorkspaceID: "ws1", Kind: "", SourceID: "x"},
		{WorkspaceID: "ws1", Kind: "waitpoint", SourceID: ""},
	}
	for _, it := range cases {
		Insert(ctx, db, quietLogger(), it)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("partial item should have been skipped; got %d rows", count)
	}
}

func TestInsert_NilDBIsNoOp(t *testing.T) {
	t.Parallel()
	// Should not panic — the early return on db == nil is exactly the
	// safety net that lets callers wire Insert into best-effort emit
	// paths without nil-guarding at each call site.
	Insert(context.Background(), nil, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "message",
		SourceID:    "x",
	})
}

func TestInsert_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	// nil logger triggers slog.Default() inside Insert — the contract
	// is "callers don't have to construct a logger if they don't care
	// about the diagnostic". Just assert no panic.
	Insert(context.Background(), db, nil, Item{
		WorkspaceID: "ws1",
		Kind:        "message",
		SourceID:    "nil-logger",
	})
}

func TestResolveBySource_FlipsStateAndStampsMetadata(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "waitpoint",
		SourceID:    "wp-99",
		Title:       "Pending",
	})

	ResolveBySource(ctx, db, quietLogger(), "waitpoint", "wp-99", "approved", "u-actor")

	var (
		state, resolvedAction sql.NullString
		resolvedByUserID      sql.NullString
		resolvedAt            sql.NullString
	)
	row := db.QueryRow(`
		SELECT state, resolved_action, resolved_by_user_id, resolved_at
		FROM inbox_items WHERE source_id = 'wp-99'`)
	if err := row.Scan(&state, &resolvedAction, &resolvedByUserID, &resolvedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !state.Valid || state.String != "resolved" {
		t.Errorf("state: want 'resolved', got %v", state)
	}
	if !resolvedAction.Valid || resolvedAction.String != "approved" {
		t.Errorf("resolved_action: want 'approved', got %v", resolvedAction)
	}
	if !resolvedByUserID.Valid || resolvedByUserID.String != "u-actor" {
		t.Errorf("resolved_by_user_id: want 'u-actor', got %v", resolvedByUserID)
	}
	if !resolvedAt.Valid {
		t.Errorf("resolved_at: should be stamped; got NULL")
	}
}

func TestResolveBySource_PreservesFirstResolution(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "escalation",
		SourceID:    "esc-double",
	})

	// First resolve sets the columns; second call must be a no-op on
	// the metadata (COALESCE keeps the original action / user / time).
	// This matters when two terminal paths race — only the first one
	// to land should own the audit record.
	ResolveBySource(ctx, db, quietLogger(), "escalation", "esc-double", "denied", "u-first")
	ResolveBySource(ctx, db, quietLogger(), "escalation", "esc-double", "approved", "u-second")

	var action, userID sql.NullString
	row := db.QueryRow(`
		SELECT resolved_action, resolved_by_user_id
		FROM inbox_items WHERE source_id = 'esc-double'`)
	if err := row.Scan(&action, &userID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if action.String != "denied" {
		t.Errorf("first-resolver-wins violated for action: got %q", action.String)
	}
	if userID.String != "u-first" {
		t.Errorf("first-resolver-wins violated for user: got %q", userID.String)
	}
}

func TestResolveBySource_NoMatchIsSilent(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	// No row exists for (kind=waitpoint, source_id=ghost). The UPDATE
	// matches zero rows and returns no error — caller's path stays
	// intact. Important because terminal paths fire from multiple
	// places (timer expiry, user click, supervisor cancel) and they
	// shouldn't have to coordinate on whether the inbox row even
	// landed in the first place.
	ResolveBySource(context.Background(), db, quietLogger(), "waitpoint", "ghost", "ignored", "")
}

func TestResolveBySource_ValidatesRequiredFields(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	// Mirror Insert's required-fields contract — empty kind or
	// source_id is a no-op, not a panic or a wildcard UPDATE.
	ResolveBySource(context.Background(), db, quietLogger(), "", "x", "a", "u")
	ResolveBySource(context.Background(), db, quietLogger(), "waitpoint", "", "a", "u")
	ResolveBySource(context.Background(), nil, quietLogger(), "waitpoint", "x", "a", "u")
}
