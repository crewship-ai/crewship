package api

// Coverage tests for feature_flags_handler.go — Update body/SetNull and
// override echo, missing-key 400s, override table error paths, and the
// isUniqueViolation(nil) contract.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covFFRig(t *testing.T) (*FeatureFlagHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewFeatureFlagHandler(db, nil, newTestLogger()), db, userID, wsID
}

func covFFSeedFlag(t *testing.T, db *sql.DB, id, key string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO feature_flags (id, key, description, enabled, percentage, created_at, updated_at)
		VALUES (?, ?, 'd', ?, 50, datetime('now'), datetime('now'))`, id, key, enabled); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
}

func covFFReq(userID, wsID, role, method, body, key string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	if key != "" {
		req.SetPathValue("key", key)
	}
	return withWorkspaceUser(req, userID, wsID, role)
}

func TestCovFFIsUniqueViolation_Nil(t *testing.T) {
	if isUniqueViolation(nil) {
		t.Error("isUniqueViolation(nil) = true, want false")
	}
}

func TestCovFFUpdate_InvalidJSON400(t *testing.T) {
	h, db, userID, wsID := covFFRig(t)
	covFFSeedFlag(t, db, "ff-1", "k1", 1)
	rec := httptest.NewRecorder()
	h.Update(rec, covFFReq(userID, wsID, "ADMIN", "PATCH", `{bad`, "k1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestCovFFUpdate_SetNullDescriptionAndOverrideEcho updates description to ""
// (NULL) on a flag carrying a workspace override; the response must show
// description null + override_enabled populated.
func TestCovFFUpdate_SetNullDescriptionAndOverrideEcho(t *testing.T) {
	h, db, userID, wsID := covFFRig(t)
	covFFSeedFlag(t, db, "ff-2", "k2", 1)
	if _, err := db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES ('ov-1', 'ff-2', ?, 0, datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Update(rec, covFFReq(userID, wsID, "ADMIN", "PATCH", `{"description":""}`, "k2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var ff featureFlagResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ff); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ff.Description != nil {
		t.Errorf("description = %v, want nil after SetNull", *ff.Description)
	}
	if ff.OverrideEnabled == nil || *ff.OverrideEnabled != false {
		t.Errorf("override_enabled = %v, want false", ff.OverrideEnabled)
	}
}

func TestCovFFMissingKey400s(t *testing.T) {
	h, _, userID, wsID := covFFRig(t)
	calls := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		meth string
	}{
		{"Delete", h.Delete, "DELETE"},
		{"UpsertOverride", h.UpsertOverride, "PUT"},
		{"DeleteOverride", h.DeleteOverride, "DELETE"},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.fn(rec, covFFReq(userID, wsID, "ADMIN", c.meth, `{"enabled":true}`, ""))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestCovFFOverride_NoWorkspace400(t *testing.T) {
	h, db, _, _ := covFFRig(t)
	covFFSeedFlag(t, db, "ff-3", "k3", 1)

	mk := func(method string) *http.Request {
		req := httptest.NewRequest(method, "/x", strings.NewReader(`{"enabled":true}`))
		req.SetPathValue("key", "k3")
		// role present, workspace empty
		return req.WithContext(withWorkspace(req.Context(), "", "ADMIN"))
	}

	rec := httptest.NewRecorder()
	h.UpsertOverride(rec, mk("PUT"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("upsert: status = %d, want 400", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.DeleteOverride(rec2, mk("DELETE"))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("delete: status = %d, want 400", rec2.Code)
	}
}

func TestCovFFOverride_TableError500(t *testing.T) {
	h, db, userID, wsID := covFFRig(t)
	covFFSeedFlag(t, db, "ff-4", "k4", 1)
	if _, err := db.Exec(`DROP TABLE feature_flag_overrides`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	t.Run("upsert 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UpsertOverride(rec, covFFReq(userID, wsID, "ADMIN", "PUT", `{"enabled":true}`, "k4"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("delete 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.DeleteOverride(rec, covFFReq(userID, wsID, "ADMIN", "DELETE", ``, "k4"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}
