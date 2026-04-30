package hooks

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestDelete_HappyPath inserts a hook and removes it, asserting the
// scope guard and rows-affected wiring.
func TestDelete_HappyPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         "PreToolUse",
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "echo ok"},
	}, true)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Delete(ctx, db, "ws_test", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := Delete(ctx, db, "ws_test", id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second Delete should return sql.ErrNoRows, got %v", err)
	}
}

// TestDelete_CrossTenantSafe confirms a delete with the wrong workspace
// is a no-op (returns ErrNoRows) — the row stays put.
func TestDelete_CrossTenantSafe(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id) VALUES ('ws_evil')`); err != nil {
		t.Fatal(err)
	}
	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         "PreToolUse",
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "echo ok"},
	}, true)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Wrong workspace — delete should miss.
	if err := Delete(ctx, db, "ws_evil", id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cross-tenant delete should miss, got %v", err)
	}
	// Real workspace — original row still there.
	got, err := Get(ctx, db, "ws_test", id)
	if err != nil || got == nil {
		t.Errorf("row should survive cross-tenant attack, got err=%v entry=%v", err, got)
	}
}

// TestSetEnabled_RoundtripsThroughDB validates the Enable/Disable
// helpers update the row and the timestamp.
func TestSetEnabled_RoundtripsThroughDB(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         "PreToolUse",
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "echo ok"},
		Enabled:       true,
	}, true)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := Disable(ctx, db, "ws_test", id); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err := Get(ctx, db, "ws_test", id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %v", err, got)
	}
	if got.Enabled {
		t.Error("expected disabled, got enabled=true")
	}

	if err := Enable(ctx, db, "ws_test", id); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got, _ = Get(ctx, db, "ws_test", id)
	if !got.Enabled {
		t.Error("expected enabled after Enable")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be stamped")
	}
	if time.Since(got.UpdatedAt) > 5*time.Second {
		t.Errorf("UpdatedAt looks stale: %v", got.UpdatedAt)
	}
}

// TestSetEnabled_MissingHook surfaces ErrNoRows.
func TestSetEnabled_MissingHook(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if err := SetEnabled(context.Background(), db, "ws_test", "h_does_not_exist", true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing hook → ErrNoRows, got %v", err)
	}
}

// TestGet_MissingReturnsNilNil — handler 404 contract.
func TestGet_MissingReturnsNilNil(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	got, err := Get(context.Background(), db, "ws_test", "h_missing")
	if err != nil {
		t.Errorf("Get missing should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("Get missing should return nil, got %+v", got)
	}
}
