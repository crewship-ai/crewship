package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
	_ "modernc.org/sqlite"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newInboxTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
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

// TestInsert_CreatedAtIsFixedWidthSortable pins the inbox created_at format:
// every writer must emit the fixed-width tsformat.Layout (9 fractional digits)
// so the (workspace_id, state, created_at DESC) index sorts by real time. A
// variable-width RFC3339Nano value (trailing fractional zeros trimmed) or the
// column's narrower strftime-ms DEFAULT would string-compare wrong against a
// fixed-width sibling inside the same second. This is the regression guard for
// the mixed-format ordering bug: the stored value must round-trip through
// tsformat.Layout AND keep its exact fixed width.
func TestInsert_CreatedAtIsFixedWidthSortable(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "message",
		SourceID:    "ts-1",
		Title:       "ts check",
	})

	var createdAt, updatedAt string
	if err := db.QueryRow(
		`SELECT created_at, updated_at FROM inbox_items WHERE source_id = 'ts-1'`).
		Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	for label, got := range map[string]string{"created_at": createdAt, "updated_at": updatedAt} {
		parsed, err := time.Parse(tsformat.Layout, got)
		if err != nil {
			t.Errorf("%s = %q does not parse as tsformat.Layout: %v", label, got, err)
			continue
		}
		// Fixed width == the width tsformat.Format itself produces. Any
		// trimmed-zero (RFC3339Nano) or strftime-ms DEFAULT value differs.
		if want := tsformat.Format(parsed); want != got {
			t.Errorf("%s = %q is not fixed-width tsformat form (re-format = %q)", label, got, want)
		}
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
	// parse failures without poisoning the inbox. Subtests isolate
	// failure attribution — if one shape regresses we want the name
	// in the report, not "case index 2 of 3".
	cases := []struct {
		name string
		item Item
	}{
		{"missing_workspace", Item{WorkspaceID: "", Kind: "waitpoint", SourceID: "x"}},
		{"missing_kind", Item{WorkspaceID: "ws1", Kind: "", SourceID: "x"}},
		{"missing_source_id", Item{WorkspaceID: "ws1", Kind: "waitpoint", SourceID: ""}},
	}
	for _, tc := range cases {
		tc := tc // capture for the subtest closure
		t.Run(tc.name, func(t *testing.T) {
			Insert(ctx, db, quietLogger(), tc.item)
		})
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

// TestResolveByPipeline_ResolvesLinkedItems pins the routine-delete cleanup:
// deleting a routine must resolve its proposed-review escalation (payload
// $.pipeline_id), its scheduled failed-run alerts (also $.pipeline_id), and
// any pending waitpoint raised mid-run (payload $.pipeline_run_id joined to
// pipeline_runs). Unrelated rows (another pipeline, another workspace) stay
// untouched.
func TestResolveByPipeline_ResolvesLinkedItems(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// A pipeline + run row that links a waitpoint back to the doomed pipeline.
	if _, err := db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pipe-A', 'ws1', 'doomed', 'Doomed', '{}', 'h1')`); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pipeline_runs (id, pipeline_id, pipeline_slug, workspace_id, status, started_at)
		VALUES ('run-A', 'pipe-A', 'doomed', 'ws1', 'RUNNING', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// Proposed-review escalation (payload carries pipeline_id) — the dangling case.
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "escalation", SourceID: "routineprop:ws1:doomed",
		Title:   "Routine proposed for review: doomed",
		Payload: map[string]interface{}{"pipeline_id": "pipe-A", "slug": "doomed"},
	})
	// Scheduled failed-run alert (payload pipeline_id).
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "failed_run", SourceID: "run-A",
		Title: "Scheduled routine failed", Payload: map[string]interface{}{"pipeline_id": "pipe-A", "run_id": "run-A"},
	})
	// Pending waitpoint (payload pipeline_run_id → pipeline_runs.pipeline_id).
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-tok",
		Title: "Approval required", Payload: map[string]interface{}{"pipeline_run_id": "run-A"},
	})
	// Unrelated: another pipeline in the same workspace — must stay unread.
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "escalation", SourceID: "routineprop:ws1:other",
		Title: "Other routine", Payload: map[string]interface{}{"pipeline_id": "pipe-B"},
	})
	// Unrelated: same pipeline_id but a DIFFERENT workspace — must stay unread.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2', 'ws2', 'ws2')`); err != nil {
		t.Fatalf("seed ws2: %v", err)
	}
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws2", Kind: "failed_run", SourceID: "run-foreign",
		Title: "Foreign", Payload: map[string]interface{}{"pipeline_id": "pipe-A"},
	})

	ResolveByPipeline(ctx, db, quietLogger(), "ws1", "pipe-A", "dismissed", "u-actor")

	resolved := func(sourceID string) bool {
		var state string
		if err := db.QueryRow(`SELECT state FROM inbox_items WHERE source_id = ?`, sourceID).Scan(&state); err != nil {
			t.Fatalf("read %s: %v", sourceID, err)
		}
		return state == "resolved"
	}
	for _, src := range []string{"routineprop:ws1:doomed", "run-A", "wp-tok"} {
		if !resolved(src) {
			t.Errorf("%s should be resolved after pipeline delete", src)
		}
	}
	for _, src := range []string{"routineprop:ws1:other", "run-foreign"} {
		if resolved(src) {
			t.Errorf("%s must NOT be resolved (different pipeline/workspace)", src)
		}
	}
	// Audit metadata stamped on a resolved row.
	var action, by sql.NullString
	if err := db.QueryRow(`SELECT resolved_action, resolved_by_user_id FROM inbox_items WHERE source_id='routineprop:ws1:doomed'`).
		Scan(&action, &by); err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if action.String != "dismissed" || by.String != "u-actor" {
		t.Errorf("audit: action=%q by=%q, want dismissed/u-actor", action.String, by.String)
	}
}

func TestResolveByPipeline_GuardsRequiredFields(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()
	// nil db / empty workspace / empty pipeline must all be no-ops (no panic).
	ResolveByPipeline(ctx, nil, quietLogger(), "ws1", "pipe-A", "dismissed", "u")
	ResolveByPipeline(ctx, db, quietLogger(), "", "pipe-A", "dismissed", "u")
	ResolveByPipeline(ctx, db, quietLogger(), "ws1", "", "dismissed", "u")
}

func TestResolveBySource_ValidatesRequiredFields(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// Seed one valid row that the invalid calls below must NOT touch.
	// Without this assertion the test only proves "no panic" — a
	// regression that accidentally turned the WHERE clause into a
	// wildcard UPDATE would flip every row in the workspace and the
	// old single-loop version would pass anyway.
	Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "waitpoint",
		SourceID:    "x",
		Title:       "pending",
	})

	// Each invalid shape gets its own subtest so failures name the
	// offending case instead of "index N." Mirror Insert's
	// required-fields contract — empty kind or source_id, or a nil
	// db handle, must short-circuit to a no-op.
	cases := []struct {
		name           string
		db             *sql.DB
		kind, sourceID string
		action, userID string
	}{
		{"missing_kind", db, "", "x", "a", "u"},
		{"missing_source_id", db, "waitpoint", "", "a", "u"},
		{"nil_db", nil, "waitpoint", "x", "a", "u"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ResolveBySource(ctx, tc.db, quietLogger(), tc.kind, tc.sourceID, tc.action, tc.userID)
		})
	}

	// The seeded row must still be unread — no invalid call should
	// have flipped its state.
	var state string
	if err := db.QueryRow(
		`SELECT state FROM inbox_items WHERE kind='waitpoint' AND source_id='x'`,
	).Scan(&state); err != nil {
		t.Fatalf("re-read seeded row: %v", err)
	}
	if state != "unread" {
		t.Errorf("invalid inputs should be no-op; got state=%q on seeded row", state)
	}
}

// --- B10: the attention contract's server-side merge (PRD-ISSUES-AND-
// ROUTINES-2026 §12, #2364) ---
//
// The next few tests characterise and then fix the exact duplicate #2364's
// live check on dev1 found: one `routine save --draft` raised BOTH the B8
// receipt ("Routine trigger ready", kind=escalation, source=
// routinetrigger:<ws>:<scheduleID>) and the older governance card ("Routine
// proposed for review", kind=escalation, source=routineprop:<ws>:<slug>) —
// two rows for one save, because the two producers shared no identity
// beyond "the same routine". See internal/api/pipeline_governance.go and
// internal/api/pipeline_trigger.go for the real call sites this reproduces.

// TestUpsert_TwoProducersSameSubject_RaiseTwoRows characterises the BUG:
// Upsert's dedup key is (kind, source_id), so two producers describing the
// same real-world subject under two different source ids raise two
// independent cards. This is what main does today — it is not a regression
// this PR introduces, it is the mechanism behind the live-observed
// duplicate, pinned here so TestWriteThreaded_MergesAcrossProducers below
// has something concrete to fix.
func TestUpsert_TwoProducersSameSubject_RaiseTwoRows(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	if err := Upsert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "escalation",
		SourceID:    "routineprop:ws1:daily-triage",
		TargetRole:  "MANAGER",
		Title:       "Routine proposed for review: daily-triage",
		Payload:     map[string]interface{}{"risk_reasons": []string{"http_egress"}},
	}); err != nil {
		t.Fatalf("upsert governance card: %v", err)
	}
	if err := Upsert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1",
		Kind:        "escalation",
		SourceID:    "routinetrigger:ws1:sched_1",
		TargetRole:  "MANAGER",
		Title:       "Routine trigger ready: daily-triage",
		Payload:     map[string]interface{}{"routine_version": 3},
	}); err != nil {
		t.Fatalf("upsert trigger-ready card: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws1' AND state != 'resolved'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("characterisation drifted: want 2 rows from the two (kind,source_id)-keyed producers, got %d", n)
	}
}

// TestWriteThreaded_MergesAcrossProducers is the fix: the SAME two producer
// calls as above, but each carrying the shared thread_key B10 gives them
// (routine:<workspace>:<slug>), now go through WriteThreaded instead of
// Upsert. They must collapse into ONE card that carries both producers'
// information — the routine_version pinned by the second call included.
func TestWriteThreaded_MergesAcrossProducers(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()
	threadKey := "routine:ws1:daily-triage"

	if err := WriteThreaded(ctx, db, quietLogger(), Item{
		WorkspaceID:    "ws1",
		Kind:           "escalation",
		SourceID:       "routineprop:ws1:daily-triage",
		TargetRole:     "MANAGER",
		Title:          "Routine proposed for review: daily-triage",
		BodyMD:         "A routine was authored that needs approval before it can run.",
		Priority:       "high",
		Blocking:       true,
		ThreadKey:      threadKey,
		AttentionClass: AttentionDecision,
		Payload:        map[string]interface{}{"risk_reasons": []string{"http_egress"}},
		Actions:        []Action{{ID: "approve_routine", Label: "Approve"}, {ID: "reject_routine", Label: "Reject"}},
	}); err != nil {
		t.Fatalf("write governance card: %v", err)
	}
	if err := WriteThreaded(ctx, db, quietLogger(), Item{
		WorkspaceID:    "ws1",
		Kind:           "escalation",
		SourceID:       "routinetrigger:ws1:sched_1",
		TargetRole:     "MANAGER",
		Title:          "Routine trigger ready: daily-triage",
		BodyMD:         "Routine daily-triage (version 3) is ready. Activate the trigger?",
		Priority:       "high",
		Blocking:       true,
		ThreadKey:      threadKey,
		AttentionClass: AttentionDecision,
		Payload:        map[string]interface{}{"routine_version": 3, "schedule_id": "sched_1"},
		Actions:        []Action{{ID: "activate_trigger", Label: "Activate"}, {ID: "dismiss_trigger", Label: "Dismiss"}},
	}); err != nil {
		t.Fatalf("write trigger-ready card: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws1' AND state != 'resolved'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("want ONE merged card across the two producers, got %d", n)
	}

	var payloadJSON, actionsJSON, bodyMD, kind, sourceID string
	if err := db.QueryRow(`SELECT kind, source_id, body_md, payload_json, actions_json FROM inbox_items WHERE workspace_id = 'ws1'`).
		Scan(&kind, &sourceID, &bodyMD, &payloadJSON, &actionsJSON); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	// Identity is the FIRST producer's — the governance card is what a later
	// Approve/Reject on the routine definition itself must still resolve.
	if kind != "escalation" || sourceID != "routineprop:ws1:daily-triage" {
		t.Errorf("merged row identity changed: kind=%q source_id=%q", kind, sourceID)
	}
	if !strings.Contains(bodyMD, "needs approval") || !strings.Contains(bodyMD, "Activate the trigger") {
		t.Errorf("merged body should carry both asks, got %q", bodyMD)
	}
	if !strings.Contains(payloadJSON, `"routine_version":3`) {
		t.Errorf("merged payload should carry routine_version from the second call, got %q", payloadJSON)
	}
	if !strings.Contains(payloadJSON, "risk_reasons") {
		t.Errorf("merged payload should still carry the first call's risk_reasons, got %q", payloadJSON)
	}
	if !strings.Contains(actionsJSON, "approve_routine") || !strings.Contains(actionsJSON, "activate_trigger") {
		t.Errorf("merged actions should union both producers' actions, got %q", actionsJSON)
	}
}

// TestWriteThreaded_RecurringCondition_OneCardAcrossFiveDays is §12's
// primary accept line: "one card across five days of the same recurring
// condition". Five separate calls under the SAME thread_key — as a daily
// sweep re-raising a still-unresolved condition would make — must never
// grow past one open row.
func TestWriteThreaded_RecurringCondition_OneCardAcrossFiveDays(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	for day := 1; day <= 5; day++ {
		if err := WriteThreaded(ctx, db, quietLogger(), Item{
			WorkspaceID:    "ws1",
			Kind:           "schedule_missed",
			SourceID:       "sched_recurring",
			TargetRole:     "MANAGER",
			Title:          "Schedule missed its window",
			BodyMD:         fmt.Sprintf("Missed occurrence, day %d.", day),
			Priority:       "high",
			ThreadKey:      "trigger:schedule:sched_recurring",
			AttentionClass: AttentionRepair,
			Payload:        map[string]interface{}{"missed_count": day},
			Actions:        []Action{{ID: "acknowledge", Label: "Acknowledge"}},
		}); err != nil {
			t.Fatalf("day %d: write threaded: %v", day, err)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("five occurrences of the same recurring condition must be ONE card, got %d", n)
	}
	var payloadJSON string
	if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&payloadJSON); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !strings.Contains(payloadJSON, `"missed_count":5`) {
		t.Errorf("the one card should reflect the LATEST occurrence, got %q", payloadJSON)
	}
}

// TestResolveByThreadOrSource_FallsBackToThread proves the resolve half of
// the merge: a decision route that only knows the SECOND producer's own
// (kind, source_id) — which was never actually written as its own row,
// because WriteThreaded merged it into the first producer's row — still
// resolves the merged card via the shared thread_key.
func TestResolveByThreadOrSource_FallsBackToThread(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()
	threadKey := "routine:ws1:daily-triage"

	if err := WriteThreaded(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "escalation", SourceID: "routineprop:ws1:daily-triage",
		Title: "Routine proposed for review", ThreadKey: threadKey,
	}); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := WriteThreaded(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "escalation", SourceID: "routinetrigger:ws1:sched_1",
		Title: "Routine trigger ready", ThreadKey: threadKey,
	}); err != nil {
		t.Fatalf("write second: %v", err)
	}

	// The trigger-activation decision route resolves by ITS OWN (kind,
	// source_id), which is not the row's stored identity — must fall back
	// to the thread.
	ResolveByThreadOrSource(ctx, db, quietLogger(), "ws1", "escalation", "routinetrigger:ws1:sched_1", threadKey, "approved", "u1")

	var state, resolvedAction string
	if err := db.QueryRow(`SELECT state, resolved_action FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&state, &resolvedAction); err != nil {
		t.Fatalf("read: %v", err)
	}
	if state != "resolved" || resolvedAction != "approved" {
		t.Errorf("thread fallback should have resolved the merged card, got state=%q action=%q", state, resolvedAction)
	}
}

// TestResolveByThreadOrSource_ExactMatchStillWorks is the ordinary case —
// no merge happened, and the caller's own (kind, source_id) is the row's
// real identity. Must behave exactly like ResolveBySource.
func TestResolveByThreadOrSource_ExactMatchStillWorks(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	if err := Insert(ctx, db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-solo", Title: "Approve",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ResolveByThreadOrSource(ctx, db, quietLogger(), "ws1", "waitpoint", "wp-solo", "", "approved", "u1")

	var state string
	if err := db.QueryRow(`SELECT state FROM inbox_items WHERE kind='waitpoint' AND source_id='wp-solo'`).Scan(&state); err != nil {
		t.Fatalf("read: %v", err)
	}
	if state != "resolved" {
		t.Errorf("want resolved, got %q", state)
	}
}

// --- B10: the digest scheduler (§12/F30, #2364) ---

// newDigestTestDB is newInboxTestDB plus one pipeline row — pipeline_runs'
// pipeline_id FK requires a real pipeline to hang runs off of. assignments'
// own FK chain (chat/agents) is heavier and not needed here: digestCounts
// sums both tables, and exercising the pipeline_runs half is enough to
// prove the sweep's SQL and merge behavior; internal/pipeline's own outcome
// tests (runs_outcome_test.go) cover the assignments half at the producer.
func newDigestTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newInboxTestDB(t)
	if _, err := db.Exec(`
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
VALUES ('pipe1', 'ws1', 'daily-triage', 'Daily Triage', '{}', 'hash1')`); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	return db
}

// seedRun inserts one pipeline_runs row with the given outcome and
// ended_at, satisfying every NOT NULL column fireOne's own writer would.
func seedRun(t *testing.T, db *sql.DB, id, outcome, endedAt string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, ended_at, outcome)
VALUES (?, 'ws1', 'pipe1', 'daily-triage', 'completed', 'run', ?, ?, ?)`,
		id, endedAt, endedAt, outcome); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}

// TestRunDigestSweepOnce_SummarizesSucceededAndNoChange is the "no card
// exists until B10" half of the red-first evidence for the digest
// scheduler: before this package had RunDigestSweepOnce, SUCCEEDED/
// NO_CHANGE runs left no inbox trace at all (F30). One sweep must
// produce exactly one card naming both counts.
func TestRunDigestSweepOnce_SummarizesSucceededAndNoChange(t *testing.T) {
	t.Parallel()
	db := newDigestTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)

	seedRun(t, db, "r1", "SUCCEEDED", recent)
	seedRun(t, db, "r2", "NO_CHANGE", recent)
	seedRun(t, db, "r3", "FAILED", recent)
	seedRun(t, db, "r4", "SUCCEEDED", recent)

	if err := RunDigestSweepOnce(ctx, db, quietLogger(), now, 24*time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws1' AND kind = 'message'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("want exactly one digest card, got %d", n)
	}
	var payloadJSON string
	if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&payloadJSON); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(payloadJSON, `"succeeded":2`) || !strings.Contains(payloadJSON, `"no_change":1`) {
		t.Errorf("digest payload should count 2 succeeded and 1 no_change (FAILED excluded), got %q", payloadJSON)
	}
}

// TestRunDigestSweepOnce_SecondSweepRefreshesTheSameCard is the digest's
// own "one card, not a new one every sweep" proof.
func TestRunDigestSweepOnce_SecondSweepRefreshesTheSameCard(t *testing.T) {
	t.Parallel()
	db := newDigestTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)

	seedRun(t, db, "r1", "SUCCEEDED", recent)
	if err := RunDigestSweepOnce(ctx, db, quietLogger(), now, 24*time.Hour); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	// A second SUCCEEDED lands before the next sweep.
	seedRun(t, db, "r2", "SUCCEEDED", recent)
	if err := RunDigestSweepOnce(ctx, db, quietLogger(), now.Add(time.Hour), 24*time.Hour); err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("want the second sweep to refresh the SAME card, got %d rows", n)
	}
	var payloadJSON string
	if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&payloadJSON); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(payloadJSON, `"succeeded":2`) {
		t.Errorf("refreshed card should reflect BOTH successes, got %q", payloadJSON)
	}
}

// TestRunDigestSweepOnce_NoActivityWritesNothing pins the other hard rule
// this package documents: an empty digest is not news.
func TestRunDigestSweepOnce_NoActivityWritesNothing(t *testing.T) {
	t.Parallel()
	db := newDigestTestDB(t)
	ctx := context.Background()
	if err := RunDigestSweepOnce(ctx, db, quietLogger(), time.Now(), 24*time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("no activity in the window should write no card, got %d", n)
	}
}

// TestRunDigestSweepOnce_OutsideWindowIsExcluded proves the window bound
// itself, not just that SOME query ran.
func TestRunDigestSweepOnce_OutsideWindowIsExcluded(t *testing.T) {
	t.Parallel()
	db := newDigestTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	seedRun(t, db, "r1", "SUCCEEDED", stale)

	if err := RunDigestSweepOnce(ctx, db, quietLogger(), now, 24*time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("a run 48h old with a 24h window should be excluded, got %d card(s)", n)
	}
}
