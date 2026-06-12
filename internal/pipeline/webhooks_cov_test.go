package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// webhooks.go — Save (create + update + validation), GetByID/GetByToken
// miss + error paths, ValidateSignature, generateWebhookID/Token shape.
// ---------------------------------------------------------------------------

func TestWebhookStore_Save_Validation(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	ctx := context.Background()

	if _, err := store.Save(ctx, SaveWebhookInput{TargetPipelineID: "p"}); err == nil || !strings.Contains(err.Error(), "workspace_id + target_pipeline_id required") {
		t.Errorf("missing workspace: %v", err)
	}
	if _, err := store.Save(ctx, SaveWebhookInput{WorkspaceID: "ws"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing pipeline: %v", err)
	}
}

func TestWebhookStore_Save_CreateMintsTokenAndDefaults(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	ctx := context.Background()

	ver := 3
	w, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID:           "ws_test",
		Name:                  "stripe-events",
		TargetPipelineID:      "pln_1",
		TargetPipelineVersion: &ver,
		SigningSecret:         "shh",
		InputsTemplate:        map[string]any{"event": "{{ body }}"},
		Enabled:               true,
		RateLimitPerMin:       60,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(w.Token, "wh_") || len(w.Token) != 3+64 {
		t.Errorf("token shape: %q", w.Token)
	}
	if w.TargetPipelineVersion == nil || *w.TargetPipelineVersion != 3 {
		t.Errorf("pinned version lost: %v", w.TargetPipelineVersion)
	}
	if w.InputsTemplateJSON != `{"event":"{{ body }}"}` {
		t.Errorf("inputs template: %q", w.InputsTemplateJSON)
	}
	if !w.Enabled || w.RateLimitPerMin != 60 {
		t.Errorf("flags lost: enabled=%v rate=%d", w.Enabled, w.RateLimitPerMin)
	}

	// Nil InputsTemplate marshals to "{}", not "null".
	w2, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID: "ws_test", Name: "n2", TargetPipelineID: "pln_1",
	})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if w2.InputsTemplateJSON != "{}" {
		t.Errorf("nil template should store {}, got %q", w2.InputsTemplateJSON)
	}
}

func TestWebhookStore_Save_UpdatePreservesToken(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	ctx := context.Background()

	w, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID: "ws_test", Name: "before", TargetPipelineID: "pln_1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	upd, err := store.Save(ctx, SaveWebhookInput{
		ID:               w.ID,
		WorkspaceID:      "ws_test",
		Name:             "after",
		TargetPipelineID: "pln_2",
		Enabled:          false,
		RateLimitPerMin:  5,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Token != w.Token {
		t.Errorf("update must preserve token: %q vs %q", upd.Token, w.Token)
	}
	if upd.Name != "after" || upd.TargetPipelineID != "pln_2" || upd.Enabled || upd.RateLimitPerMin != 5 {
		t.Errorf("update fields not applied: %+v", upd)
	}
}

func TestWebhookStore_GetByToken_Misses(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	ctx := context.Background()

	if _, err := store.GetByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty token: %v", err)
	}
	if _, err := store.GetByToken(ctx, "wh_unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := store.GetByID(ctx, "pwh_unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestWebhookStore_ClosedDB_ErrorPaths(t *testing.T) {
	db := openWebhookTestDB(t)
	store := NewWebhookStore(db)
	ctx := context.Background()
	_ = db.Close()

	if _, err := store.Save(ctx, SaveWebhookInput{WorkspaceID: "ws", TargetPipelineID: "p"}); err == nil || !strings.Contains(err.Error(), "insert webhook") {
		t.Errorf("Save insert: %v", err)
	}
	if _, err := store.Save(ctx, SaveWebhookInput{ID: "pwh_x", WorkspaceID: "ws", TargetPipelineID: "p"}); err == nil || !strings.Contains(err.Error(), "update webhook") {
		t.Errorf("Save update: %v", err)
	}
	if _, err := store.GetByID(ctx, "x"); err == nil {
		t.Error("GetByID should error on closed DB")
	}
	if _, err := store.GetByToken(ctx, "wh_x"); err == nil {
		t.Error("GetByToken should error on closed DB")
	}
	if _, err := store.List(ctx, "ws"); err == nil {
		t.Error("List should error on closed DB")
	}
	if err := store.SoftDelete(ctx, "x"); err == nil {
		t.Error("SoftDelete should error on closed DB")
	}
	if err := store.RecordFire(ctx, "x", "r", "s"); err == nil {
		t.Error("RecordFire should error on closed DB")
	}
}

func TestWebhook_ValidateSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"x"}`)

	// No secret on the row → every dispatch is unauthenticated.
	w := &Webhook{}
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	digest := hex.EncodeToString(mac.Sum(nil))
	if w.ValidateSignature(body, digest) {
		t.Error("empty secret must reject")
	}

	w.SigningSecret = "secret"
	if w.ValidateSignature(body, "") {
		t.Error("empty provided digest must reject")
	}
	if !w.ValidateSignature(body, digest) {
		t.Error("valid digest must pass")
	}
	if w.ValidateSignature(body, strings.Repeat("0", 64)) {
		t.Error("wrong digest must reject")
	}
	if w.ValidateSignature([]byte("tampered"), digest) {
		t.Error("tampered body must reject")
	}
}

// TestScanWebhook_DeletedAtBranch scans a soft-deleted row directly —
// the public lookups all filter deleted rows, so the DeletedAt decode
// branch is unreachable through them.
func TestScanWebhook_DeletedAtBranch(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	ctx := context.Background()

	w, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID: "ws_test", Name: "doomed", TargetPipelineID: "pln_1",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SoftDelete(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, err := db.Query(webhookSelect+` WHERE id = ?`, w.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row missing")
	}
	got, err := scanWebhook(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.DeletedAt == nil {
		t.Error("deleted_at lost in scan")
	}
	if got.Enabled {
		t.Error("soft delete must disable")
	}
}

func TestGenerateWebhookID_Format(t *testing.T) {
	t.Parallel()
	id1 := generateWebhookID()
	id2 := generateWebhookID()
	if !strings.HasPrefix(id1, "pwh_c") {
		t.Errorf("prefix: %q", id1)
	}
	if id1 == id2 {
		t.Error("ids must be unique")
	}
}
