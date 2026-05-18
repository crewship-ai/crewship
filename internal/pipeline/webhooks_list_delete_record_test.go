package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// webhooks.go — WebhookStore.List, SoftDelete, RecordFire.
//
// Save / GetByID / GetByToken / ValidateSignature / AllowWebhookFire are
// already covered by sibling tests. This fills the three CRUD/audit
// methods the admin UI + dispatcher rely on.
// ---------------------------------------------------------------------------

func saveWebhookForTest(t *testing.T, store *WebhookStore, wsID, name, pipelineID string) *Webhook {
	t.Helper()
	w, err := store.Save(context.Background(), SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             name,
		TargetPipelineID: pipelineID,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save webhook %s: %v", name, err)
	}
	return w
}

// ---- List ----

func TestWebhookStore_List_EmptyWorkspace_ReturnsNil(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)

	got, err := store.List(context.Background(), "ws_no_hooks")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty workspace", got)
	}
}

func TestWebhookStore_List_OrdersByCreatedAtDesc(t *testing.T) {
	// Source: "ORDER BY created_at DESC". Pin that the latest-created
	// webhook lands first (admin UI shows newest at top).
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_list", "list-target")
	store := NewWebhookStore(db)

	// Insert three; stagger created_at via direct UPDATE so the order
	// is deterministic regardless of SQLite's subsec resolution.
	first := saveWebhookForTest(t, store, "ws_list", "alpha", "pln_list")
	second := saveWebhookForTest(t, store, "ws_list", "beta", "pln_list")
	third := saveWebhookForTest(t, store, "ws_list", "gamma", "pln_list")
	for i, id := range []string{first.ID, second.ID, third.ID} {
		// 100ms apart, oldest first.
		ts := time.Now().UTC().Add(time.Duration(i) * 100 * time.Millisecond).Format(time.RFC3339Nano)
		if _, err := db.Exec(`UPDATE pipeline_webhooks SET created_at = ? WHERE id = ?`, ts, id); err != nil {
			t.Fatalf("stagger created_at: %v", err)
		}
	}

	got, err := store.List(context.Background(), "ws_list")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d webhooks, want 3", len(got))
	}
	wantOrder := []string{third.ID, second.ID, first.ID}
	for i, w := range got {
		if w.ID != wantOrder[i] {
			t.Errorf("List[%d] = %s, want %s (DESC by created_at)", i, w.ID, wantOrder[i])
		}
	}
}

func TestWebhookStore_List_HidesSoftDeleted(t *testing.T) {
	// Source query: "WHERE workspace_id = ? AND deleted_at IS NULL".
	// A soft-deleted webhook must vanish from List entirely.
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_hide", "hide-target")
	store := NewWebhookStore(db)

	keep := saveWebhookForTest(t, store, "ws_hide", "keep", "pln_hide")
	gone := saveWebhookForTest(t, store, "ws_hide", "gone", "pln_hide")
	if err := store.SoftDelete(context.Background(), gone.ID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	got, err := store.List(context.Background(), "ws_hide")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d webhooks, want 1 (soft-deleted excluded)", len(got))
	}
	if got[0].ID != keep.ID {
		t.Errorf("survivor = %s, want %s", got[0].ID, keep.ID)
	}
}

func TestWebhookStore_List_ScopedByWorkspace(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_scope", "scope-target")
	store := NewWebhookStore(db)

	_ = saveWebhookForTest(t, store, "ws_scope_a", "ours", "pln_scope")
	_ = saveWebhookForTest(t, store, "ws_scope_b", "theirs", "pln_scope")

	got, err := store.List(context.Background(), "ws_scope_a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ours" {
		t.Errorf("got %+v, want exactly one row named \"ours\" (foreign workspace excluded)", got)
	}
}

// ---- SoftDelete ----

func TestWebhookStore_SoftDelete_SetsFieldsAndDisables(t *testing.T) {
	// Source: SET deleted_at, updated_at, enabled = 0. The dispatcher's
	// enabled-flag check is the runtime guard; deleted_at is the audit
	// signal. Pin all three.
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_sd", "sd-target")
	store := NewWebhookStore(db)

	wh := saveWebhookForTest(t, store, "ws_sd", "to-delete", "pln_sd")

	if err := store.SoftDelete(context.Background(), wh.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	var enabled int
	var deletedAt, updatedAt string
	if err := db.QueryRow(`SELECT enabled, deleted_at, updated_at FROM pipeline_webhooks WHERE id = ?`, wh.ID).
		Scan(&enabled, &deletedAt, &updatedAt); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if enabled != 0 {
		t.Errorf("enabled = %d after SoftDelete, want 0", enabled)
	}
	if deletedAt == "" {
		t.Error("deleted_at empty after SoftDelete")
	}
	if updatedAt == "" {
		t.Error("updated_at not set on SoftDelete (the audit timestamp)")
	}
}

func TestWebhookStore_SoftDelete_UnknownID_ErrNotFound(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	err := store.SoftDelete(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestWebhookStore_SoftDelete_AlreadyDeleted_ErrNotFound(t *testing.T) {
	// The UPDATE filter is `deleted_at IS NULL` — re-deleting must
	// match zero rows and surface ErrNotFound. Idempotent at the UI
	// level (the UI hides deleted rows so the call shouldn't happen
	// twice in practice), but the error signal is the contract.
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_ad", "ad-target")
	store := NewWebhookStore(db)
	wh := saveWebhookForTest(t, store, "ws_ad", "ad", "pln_ad")

	if err := store.SoftDelete(context.Background(), wh.ID); err != nil {
		t.Fatalf("first SoftDelete: %v", err)
	}
	err := store.SoftDelete(context.Background(), wh.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second SoftDelete err = %v, want ErrNotFound", err)
	}
}

// ---- RecordFire ----

func TestWebhookStore_RecordFire_UpdatesLastFiredAtStatusRunIDAndCount(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_rf", "rf-target")
	store := NewWebhookStore(db)
	wh := saveWebhookForTest(t, store, "ws_rf", "rf", "pln_rf")

	before := time.Now().UTC()
	if err := store.RecordFire(context.Background(), wh.ID, "run-abc-1", "COMPLETED"); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}
	after := time.Now().UTC().Add(time.Second) // tolerate sub-second jitter

	var lastFiredAt, lastStatus, lastRunID string
	var fireCount int64
	if err := db.QueryRow(`SELECT last_fired_at, last_status, last_run_id, fire_count FROM pipeline_webhooks WHERE id = ?`, wh.ID).
		Scan(&lastFiredAt, &lastStatus, &lastRunID, &fireCount); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if lastStatus != "COMPLETED" {
		t.Errorf("last_status = %q, want COMPLETED", lastStatus)
	}
	if lastRunID != "run-abc-1" {
		t.Errorf("last_run_id = %q, want run-abc-1", lastRunID)
	}
	if fireCount != 1 {
		t.Errorf("fire_count = %d, want 1", fireCount)
	}
	parsed, err := time.Parse(time.RFC3339Nano, lastFiredAt)
	if err != nil {
		t.Fatalf("parse last_fired_at: %v (%q)", err, lastFiredAt)
	}
	if parsed.Before(before.Add(-time.Second)) || parsed.After(after) {
		t.Errorf("last_fired_at = %v, want in [%v, %v]", parsed, before, after)
	}
}

func TestWebhookStore_RecordFire_FireCountIncrementsPerCall(t *testing.T) {
	// `fire_count = fire_count + 1` — pin that successive RecordFire
	// calls accumulate; a regression to assignment would lose the
	// dispatch history audit.
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_inc", "inc-target")
	store := NewWebhookStore(db)
	wh := saveWebhookForTest(t, store, "ws_inc", "inc", "pln_inc")

	for i := 1; i <= 5; i++ {
		if err := store.RecordFire(context.Background(), wh.ID, "run-i", "COMPLETED"); err != nil {
			t.Fatalf("RecordFire #%d: %v", i, err)
		}
		var n int64
		if err := db.QueryRow(`SELECT fire_count FROM pipeline_webhooks WHERE id = ?`, wh.ID).Scan(&n); err != nil {
			t.Fatalf("read fire_count after fire #%d: %v", i, err)
		}
		if int(n) != i {
			t.Errorf("after fire #%d: fire_count = %d, want %d", i, n, i)
		}
	}
}

func TestWebhookStore_RecordFire_EmptyRunIDStoresNull(t *testing.T) {
	// runID is passed through nullStr — empty becomes NULL in the DB
	// (not the empty string). The downstream admin-UI display logic
	// distinguishes "no run" from "empty run id".
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pln_null", "null-target")
	store := NewWebhookStore(db)
	wh := saveWebhookForTest(t, store, "ws_null", "null", "pln_null")

	if err := store.RecordFire(context.Background(), wh.ID, "", "FAILED"); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}
	var lastRunID interface{}
	if err := db.QueryRow(`SELECT last_run_id FROM pipeline_webhooks WHERE id = ?`, wh.ID).Scan(&lastRunID); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if lastRunID != nil {
		t.Errorf("last_run_id = %v, want SQL NULL (empty runID → nullStr → NULL)", lastRunID)
	}
}

func TestWebhookStore_RecordFire_UnknownID_NoError(t *testing.T) {
	// RecordFire's UPDATE doesn't check rows-affected — an unknown id
	// is silently a no-op. Pin the documented behavior so a refactor
	// to ErrNotFound (which would break the dispatch path that races
	// with delete) doesn't slip in unnoticed.
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	if err := store.RecordFire(context.Background(), "missing", "run-x", "COMPLETED"); err != nil {
		t.Errorf("unknown id should not error; got %v", err)
	}
}
