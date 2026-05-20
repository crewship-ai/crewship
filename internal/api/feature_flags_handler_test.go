package api

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// ── Test fixtures ──────────────────────────────────────────────────────────

// newFeatureFlagHandler returns a handler wired against an in-memory DB
// plus a seeded user + workspace. Callers receive the user/ws IDs so they
// can drive context-builders directly.
func newFeatureFlagHandler(t *testing.T) (*FeatureFlagHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewFeatureFlagHandler(db, nil, logger), userID, wsID
}

// withFFCtx is the standard "authenticated + workspace-scoped" context
// builder used by every test in this file.
func withFFCtx(userID, wsID, role string) context.Context {
	return withWorkspace(withUser(context.Background(), &AuthUser{ID: userID}), wsID, role)
}

// createTestFlag inserts a flag definition directly into the DB so tests
// don't have to go through the (also-tested) Create handler when they're
// exercising List/Update/Delete/Override behavior.
func createTestFlag(t *testing.T, db *sql.DB, key string, enabled bool, percentage int) string {
	t.Helper()
	id := generateCUID()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := db.Exec(`
		INSERT INTO feature_flags (id, key, description, enabled, percentage, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, key, "test flag", enabledInt, percentage)
	if err != nil {
		t.Fatalf("seed feature flag: %v", err)
	}
	return id
}

// dbFromHandler reaches into the handler to fetch the underlying *sql.DB
// for direct row inspection. We need this because the seed helpers take
// a *sql.DB and the test setup wraps it inside the handler.
func dbFromHandler(h *FeatureFlagHandler) *sql.DB { return h.db }

// ── List ───────────────────────────────────────────────────────────────────

func TestFeatureFlag_List_Empty(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/feature-flags", nil)
	req = req.WithContext(withFFCtx(userID, wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []featureFlagResponse
	mustUnmarshal(t, rr, &got)
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}
}

func TestFeatureFlag_List_WithFlagsAndOverride(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	db := dbFromHandler(h)

	flagAID := createTestFlag(t, db, "alpha", true, 0)
	createTestFlag(t, db, "beta", false, 50)

	// Attach override only for `alpha` and only for our workspace.
	_, err := db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES (?, ?, ?, 0, datetime('now'))`, generateCUID(), flagAID, wsID)
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/feature-flags", nil)
	req = req.WithContext(withFFCtx(userID, wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []featureFlagResponse
	mustUnmarshal(t, rr, &got)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Sorted alphabetically by key.
	if got[0].Key != "alpha" || got[1].Key != "beta" {
		t.Errorf("ordering wrong: %v", []string{got[0].Key, got[1].Key})
	}
	// alpha has an override (false), beta does not.
	if got[0].OverrideEnabled == nil || *got[0].OverrideEnabled != false {
		t.Errorf("alpha override = %v, want pointer-to-false", got[0].OverrideEnabled)
	}
	if got[1].OverrideEnabled != nil {
		t.Errorf("beta override = %v, want nil", got[1].OverrideEnabled)
	}
	if got[0].Enabled != true {
		t.Errorf("alpha enabled (default) = %v, want true", got[0].Enabled)
	}
}

// Override rows from OTHER workspaces must not leak into this workspace's List.
func TestFeatureFlag_List_OtherWorkspaceOverride_NotLeaked(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	db := dbFromHandler(h)

	flagID := createTestFlag(t, db, "gamma", false, 0)

	// Seed a second workspace + override.
	_, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`)
	if err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES (?, ?, 'ws-other', 1, datetime('now'))`, generateCUID(), flagID)
	if err != nil {
		t.Fatalf("seed other override: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/feature-flags", nil)
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var got []featureFlagResponse
	mustUnmarshal(t, rr, &got)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].OverrideEnabled != nil {
		t.Errorf("override leaked from other workspace: %v", *got[0].OverrideEnabled)
	}
}

// ── Create ─────────────────────────────────────────────────────────────────

func TestFeatureFlag_Create_OK(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	body := bytes.NewBufferString(`{"key":"new-flag","description":"docs","enabled":true,"percentage":25}`)
	req := httptest.NewRequest("POST", "/api/v1/feature-flags", body)
	req = req.WithContext(withFFCtx(userID, wsID, "ADMIN"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got featureFlagResponse
	mustUnmarshal(t, rr, &got)
	if got.Key != "new-flag" {
		t.Errorf("key = %q, want new-flag", got.Key)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true")
	}
	if got.Percentage != 25 {
		t.Errorf("percentage = %d, want 25", got.Percentage)
	}
	if got.ID == "" {
		t.Error("id not set")
	}

	// Verify the row landed in DB with enabled=1.
	var enabled int
	err := dbFromHandler(h).QueryRow(`SELECT enabled FROM feature_flags WHERE key = ?`, "new-flag").Scan(&enabled)
	if err != nil {
		t.Fatalf("query flag: %v", err)
	}
	if enabled != 1 {
		t.Errorf("DB enabled = %d, want 1", enabled)
	}
}

func TestFeatureFlag_Create_Validations(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty key", `{"key":"","enabled":true,"percentage":0}`, http.StatusBadRequest},
		{"percentage too high", `{"key":"ok","percentage":101}`, http.StatusBadRequest},
		{"percentage negative", `{"key":"ok","percentage":-1}`, http.StatusBadRequest},
		{"bad json", `not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/feature-flags", bytes.NewBufferString(tc.body))
			req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestFeatureFlag_Create_DuplicateKey(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "dupe", false, 0)

	body := bytes.NewBufferString(`{"key":"dupe","percentage":0}`)
	req := httptest.NewRequest("POST", "/api/v1/feature-flags", body)
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

// ── Update ─────────────────────────────────────────────────────────────────

func TestFeatureFlag_Update_OK(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "tweak-me", false, 10)

	body := bytes.NewBufferString(`{"enabled":true,"percentage":80,"description":"now better"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/tweak-me", body)
	req.SetPathValue("key", "tweak-me")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got featureFlagResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || got.Percentage != 80 {
		t.Errorf("post-update: enabled=%v pct=%d, want true/80", got.Enabled, got.Percentage)
	}
	if got.Description == nil || *got.Description != "now better" {
		t.Errorf("description = %v, want 'now better'", got.Description)
	}
}

func TestFeatureFlag_Update_NotFound(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/missing", bytes.NewBufferString(`{"enabled":true}`))
	req.SetPathValue("key", "missing")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestFeatureFlag_Update_NoFields(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "stable", true, 100)

	req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/stable", bytes.NewBufferString(`{}`))
	req.SetPathValue("key", "stable")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestFeatureFlag_Update_PercentageBounds(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "bounds", false, 50)

	for _, body := range []string{`{"percentage":-1}`, `{"percentage":101}`} {
		req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/bounds", bytes.NewBufferString(body))
		req.SetPathValue("key", "bounds")
		req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%s status = %d, want 400", body, rr.Code)
		}
	}
}

// ── Delete ─────────────────────────────────────────────────────────────────

func TestFeatureFlag_Delete_OK(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "doomed", true, 0)

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/doomed", nil)
	req.SetPathValue("key", "doomed")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Verify removal.
	var count int
	dbFromHandler(h).QueryRow(`SELECT COUNT(*) FROM feature_flags WHERE key = ?`, "doomed").Scan(&count)
	if count != 0 {
		t.Errorf("flag still present after delete (count=%d)", count)
	}
}

// Verify the ON DELETE CASCADE actually drops attached override rows so
// callers don't need to do a two-step delete.
func TestFeatureFlag_Delete_CascadesOverrides(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	db := dbFromHandler(h)
	flagID := createTestFlag(t, db, "with-override", false, 0)
	_, err := db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES (?, ?, ?, 1, datetime('now'))`, generateCUID(), flagID, wsID)
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/with-override", nil)
	req.SetPathValue("key", "with-override")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}

	var overrideCount int
	db.QueryRow(`SELECT COUNT(*) FROM feature_flag_overrides WHERE flag_id = ?`, flagID).Scan(&overrideCount)
	if overrideCount != 0 {
		t.Errorf("override rows not cascaded: count=%d", overrideCount)
	}
}

func TestFeatureFlag_Delete_NotFound(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/missing", nil)
	req.SetPathValue("key", "missing")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── UpsertOverride ─────────────────────────────────────────────────────────

func TestFeatureFlag_UpsertOverride_Insert(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	flagID := createTestFlag(t, dbFromHandler(h), "needs-override", false, 0)

	body := bytes.NewBufferString(`{"enabled":true}`)
	req := httptest.NewRequest("PUT", "/api/v1/feature-flags/needs-override/override", body)
	req.SetPathValue("key", "needs-override")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpsertOverride(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var enabled int
	err := dbFromHandler(h).QueryRow(`SELECT enabled FROM feature_flag_overrides
		WHERE flag_id = ? AND workspace_id = ?`, flagID, wsID).Scan(&enabled)
	if err != nil {
		t.Fatalf("query override: %v", err)
	}
	if enabled != 1 {
		t.Errorf("override enabled = %d, want 1", enabled)
	}
}

// PUT twice must update the same row, not create a duplicate.
// This exercises the UNIQUE(flag_id, workspace_id) UPSERT path.
func TestFeatureFlag_UpsertOverride_UpsertReusesRow(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	flagID := createTestFlag(t, dbFromHandler(h), "toggle-me", false, 0)

	// First PUT: enabled=true.
	doPUT := func(enabled bool) {
		var body string
		if enabled {
			body = `{"enabled":true}`
		} else {
			body = `{"enabled":false}`
		}
		req := httptest.NewRequest("PUT", "/api/v1/feature-flags/toggle-me/override", bytes.NewBufferString(body))
		req.SetPathValue("key", "toggle-me")
		req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.UpsertOverride(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT enabled=%v: %d body=%s", enabled, rr.Code, rr.Body.String())
		}
	}
	doPUT(true)
	doPUT(false)
	doPUT(true)

	// Exactly one row should exist.
	var count int
	dbFromHandler(h).QueryRow(`SELECT COUNT(*) FROM feature_flag_overrides
		WHERE flag_id = ? AND workspace_id = ?`, flagID, wsID).Scan(&count)
	if count != 1 {
		t.Errorf("override row count = %d, want 1 (UPSERT should reuse the row)", count)
	}

	// And it should reflect the LAST write (enabled=true).
	var enabled int
	dbFromHandler(h).QueryRow(`SELECT enabled FROM feature_flag_overrides
		WHERE flag_id = ? AND workspace_id = ?`, flagID, wsID).Scan(&enabled)
	if enabled != 1 {
		t.Errorf("final enabled = %d, want 1", enabled)
	}
}

func TestFeatureFlag_UpsertOverride_FlagNotFound(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	req := httptest.NewRequest("PUT", "/api/v1/feature-flags/missing/override", bytes.NewBufferString(`{"enabled":true}`))
	req.SetPathValue("key", "missing")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpsertOverride(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestFeatureFlag_UpsertOverride_BadJSON(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "ok-flag", false, 0)

	req := httptest.NewRequest("PUT", "/api/v1/feature-flags/ok-flag/override", bytes.NewBufferString(`not json`))
	req.SetPathValue("key", "ok-flag")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpsertOverride(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── DeleteOverride ─────────────────────────────────────────────────────────

func TestFeatureFlag_DeleteOverride_OK(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	db := dbFromHandler(h)
	flagID := createTestFlag(t, db, "to-clear", false, 0)
	_, err := db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES (?, ?, ?, 1, datetime('now'))`, generateCUID(), flagID, wsID)
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/to-clear/override", nil)
	req.SetPathValue("key", "to-clear")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.DeleteOverride(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM feature_flag_overrides
		WHERE flag_id = ? AND workspace_id = ?`, flagID, wsID).Scan(&count)
	if count != 0 {
		t.Errorf("override row still present after delete (count=%d)", count)
	}
}

// Deleting a non-existent override on an existing flag is treated as
// idempotent success (204) — repeated "inherit default" calls shouldn't
// blow up.
func TestFeatureFlag_DeleteOverride_NoExistingRowIsOK(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)
	createTestFlag(t, dbFromHandler(h), "no-override", false, 0)

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/no-override/override", nil)
	req.SetPathValue("key", "no-override")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.DeleteOverride(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (idempotent inherit)", rr.Code)
	}
}

func TestFeatureFlag_DeleteOverride_FlagNotFound(t *testing.T) {
	h, userID, wsID := newFeatureFlagHandler(t)

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/missing/override", nil)
	req.SetPathValue("key", "missing")
	req = req.WithContext(withFFCtx(userID, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.DeleteOverride(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (flag itself missing)", rr.Code)
	}
}

// ── RBAC matrix ────────────────────────────────────────────────────────────

// rbacCase wires a role to the expected status code for a given verb.
// Read endpoints (List) should accept everyone authenticated.
// Mutating endpoints (Create/Update/Delete/UpsertOverride/DeleteOverride)
// require OWNER or ADMIN — anything else gets a 403.
type rbacCase struct {
	role       string
	wantList   int
	wantCreate int
	wantUpdate int
	wantDelete int
	wantUpsert int // PUT override
	wantClear  int // DELETE override
}

func TestFeatureFlag_RBAC_Matrix(t *testing.T) {
	matrix := []rbacCase{
		{role: "OWNER", wantList: http.StatusOK, wantCreate: http.StatusCreated, wantUpdate: http.StatusOK, wantDelete: http.StatusNoContent, wantUpsert: http.StatusOK, wantClear: http.StatusNoContent},
		{role: "ADMIN", wantList: http.StatusOK, wantCreate: http.StatusCreated, wantUpdate: http.StatusOK, wantDelete: http.StatusNoContent, wantUpsert: http.StatusOK, wantClear: http.StatusNoContent},
		{role: "MANAGER", wantList: http.StatusOK, wantCreate: http.StatusForbidden, wantUpdate: http.StatusForbidden, wantDelete: http.StatusForbidden, wantUpsert: http.StatusForbidden, wantClear: http.StatusForbidden},
		{role: "MEMBER", wantList: http.StatusOK, wantCreate: http.StatusForbidden, wantUpdate: http.StatusForbidden, wantDelete: http.StatusForbidden, wantUpsert: http.StatusForbidden, wantClear: http.StatusForbidden},
		{role: "VIEWER", wantList: http.StatusOK, wantCreate: http.StatusForbidden, wantUpdate: http.StatusForbidden, wantDelete: http.StatusForbidden, wantUpsert: http.StatusForbidden, wantClear: http.StatusForbidden},
		// Empty role = unauthenticated-ish state. canRole rejects "" for
		// EVERY action including "read" as defense in depth — so even List
		// returns 403. The handler now gates List on requireRole("read")
		// to make this real instead of just an aspirational comment.
		{role: "", wantList: http.StatusForbidden, wantCreate: http.StatusForbidden, wantUpdate: http.StatusForbidden, wantDelete: http.StatusForbidden, wantUpsert: http.StatusForbidden, wantClear: http.StatusForbidden},
	}

	for _, tc := range matrix {
		t.Run("role="+tc.role, func(t *testing.T) {
			h, userID, wsID := newFeatureFlagHandler(t)

			// Pre-seed a flag for Update/Delete/Override paths.
			createTestFlag(t, dbFromHandler(h), "rbac-flag", false, 0)

			// List
			{
				req := httptest.NewRequest("GET", "/api/v1/feature-flags", nil)
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.List(rr, req)
				if rr.Code != tc.wantList {
					t.Errorf("List: got %d, want %d body=%s", rr.Code, tc.wantList, rr.Body.String())
				}
			}

			// Create
			{
				req := httptest.NewRequest("POST", "/api/v1/feature-flags",
					bytes.NewBufferString(`{"key":"created-by-`+tc.role+`","percentage":0}`))
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.Create(rr, req)
				if rr.Code != tc.wantCreate {
					t.Errorf("Create: got %d, want %d body=%s", rr.Code, tc.wantCreate, rr.Body.String())
				}
			}

			// Update (on the seeded rbac-flag)
			{
				req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/rbac-flag",
					bytes.NewBufferString(`{"enabled":true}`))
				req.SetPathValue("key", "rbac-flag")
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.Update(rr, req)
				if rr.Code != tc.wantUpdate {
					t.Errorf("Update: got %d, want %d body=%s", rr.Code, tc.wantUpdate, rr.Body.String())
				}
			}

			// UpsertOverride (on the seeded rbac-flag)
			{
				req := httptest.NewRequest("PUT", "/api/v1/feature-flags/rbac-flag/override",
					bytes.NewBufferString(`{"enabled":true}`))
				req.SetPathValue("key", "rbac-flag")
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.UpsertOverride(rr, req)
				if rr.Code != tc.wantUpsert {
					t.Errorf("UpsertOverride: got %d, want %d body=%s", rr.Code, tc.wantUpsert, rr.Body.String())
				}
			}

			// DeleteOverride (on the seeded rbac-flag)
			{
				req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/rbac-flag/override", nil)
				req.SetPathValue("key", "rbac-flag")
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.DeleteOverride(rr, req)
				if rr.Code != tc.wantClear {
					t.Errorf("DeleteOverride: got %d, want %d body=%s", rr.Code, tc.wantClear, rr.Body.String())
				}
			}

			// Delete (last — destroys the seeded flag)
			{
				req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/rbac-flag", nil)
				req.SetPathValue("key", "rbac-flag")
				req = req.WithContext(withFFCtx(userID, wsID, tc.role))
				rr := httptest.NewRecorder()
				h.Delete(rr, req)
				if rr.Code != tc.wantDelete {
					t.Errorf("Delete: got %d, want %d body=%s", rr.Code, tc.wantDelete, rr.Body.String())
				}
			}
		})
	}
}
