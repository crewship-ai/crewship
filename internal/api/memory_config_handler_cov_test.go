package api

// Coverage tests for memory_config_handler.go — SetJournal nil guard,
// Patch body/no-op/diff branches, emit-failure logging, loadConfigDoc
// error paths, and the jsonIntValue / jsonEqual type matrix.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func covMCRig(t *testing.T) (*MemoryConfigHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewMemoryConfigHandler(db, newTestLogger()), db, userID, wsID
}

func covMCReq(userID, wsID, method, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/admin/memory/config", strings.NewReader(body))
	return withWorkspaceUser(req, userID, wsID, "OWNER")
}

// failingEmitter always errors on Emit; drives the audit-emit-failure log path.
type covMCFailingEmitter struct{}

func (covMCFailingEmitter) Emit(context.Context, journal.Entry) (string, error) {
	return "", errors.New("journal down")
}
func (covMCFailingEmitter) Flush(context.Context) error { return nil }

func TestCovMCSetJournal(t *testing.T) {
	h, _, _, _ := covMCRig(t)
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Errorf("SetJournal(nil) should install noopEmitter, got %T", h.journal)
	}
	h.SetJournal(covMCFailingEmitter{})
	if _, ok := h.journal.(covMCFailingEmitter); !ok {
		t.Errorf("SetJournal should install the emitter, got %T", h.journal)
	}
}

func TestCovMCPatch_BodyGuards(t *testing.T) {
	h, _, userID, wsID := covMCRig(t)

	t.Run("null body 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Patch(rec, covMCReq(userID, wsID, "PATCH", `null`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("trailing garbage 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Patch(rec, covMCReq(userID, wsID, "PATCH", `{"versions_retention_days":7} JUNK`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

// TestCovMCPatch_ChangeExistingValue updates an already-set key — the diff
// must carry the previous value, the column must update, and a failing
// journal emitter must NOT fail the request.
func TestCovMCPatch_ChangeExistingValue(t *testing.T) {
	h, db, userID, wsID := covMCRig(t)
	h.SetJournal(covMCFailingEmitter{})
	if _, err := db.Exec(`UPDATE workspaces SET memory_config = '{"versions_retention_days":30}' WHERE id = ?`, wsID); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Patch(rec, covMCReq(userID, wsID, "PATCH", `{"versions_retention_days":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp memoryConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VersionsRetentionDays != 7 || resp.IsDefault {
		t.Errorf("resp = %+v, want retention 7 / is_default false", resp)
	}
	var raw string
	if err := db.QueryRow(`SELECT memory_config FROM workspaces WHERE id = ?`, wsID).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(raw, `"versions_retention_days":7`) {
		t.Errorf("stored config = %q", raw)
	}
}

func TestCovMCPatch_NoOpSkipsWrite(t *testing.T) {
	h, db, userID, wsID := covMCRig(t)
	if _, err := db.Exec(`UPDATE workspaces SET memory_config = '{"versions_retention_days":30}' WHERE id = ?`, wsID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Patch(rec, covMCReq(userID, wsID, "PATCH", `{"versions_retention_days":30}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp memoryConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VersionsRetentionDays != 30 {
		t.Errorf("retention = %d, want 30", resp.VersionsRetentionDays)
	}
}

func TestCovMCPatch_WorkspaceNotFound500(t *testing.T) {
	h, _, userID, _ := covMCRig(t)
	rec := httptest.NewRecorder()
	h.Patch(rec, covMCReq(userID, "ws-ghost", "PATCH", `{"versions_retention_days":7}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovMCPatch_BeginTxError500(t *testing.T) {
	h, db, userID, wsID := covMCRig(t)
	db.Close()
	rec := httptest.NewRecorder()
	h.Patch(rec, covMCReq(userID, wsID, "PATCH", `{"versions_retention_days":7}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovMCGet_ErrorPaths(t *testing.T) {
	t.Run("workspace not found 500", func(t *testing.T) {
		h, _, userID, _ := covMCRig(t)
		rec := httptest.NewRecorder()
		h.Get(rec, covMCReq(userID, "ws-ghost", "GET", ""))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
	t.Run("db error 500", func(t *testing.T) {
		h, db, userID, wsID := covMCRig(t)
		db.Close()
		rec := httptest.NewRecorder()
		h.Get(rec, covMCReq(userID, wsID, "GET", ""))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

func TestCovMCJSONIntValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{"float64 whole", float64(7), 7, true},
		{"float64 fractional", 7.5, 0, false},
		{"int", int(9), 9, true},
		{"int64", int64(11), 11, true},
		{"json.Number int", json.Number("13"), 13, true},
		{"json.Number fractional", json.Number("1.5"), 0, false},
		{"string", "7", 0, false},
		{"bool", true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := jsonIntValue(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("jsonIntValue(%v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCovMCJSONEqual(t *testing.T) {
	if !jsonEqual(float64(7), json.Number("7")) {
		t.Error("float64(7) vs Number(7) should be wire-equal")
	}
	if jsonEqual(7, 8) {
		t.Error("7 vs 8 should differ")
	}
	// Unmarshallable value → false (marshal error branch).
	if jsonEqual(make(chan int), 1) {
		t.Error("unmarshallable value must compare unequal")
	}
}
