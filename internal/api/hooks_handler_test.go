package api

// Tests for the hooks registry endpoints (List / Enable / Disable).
//
// Coverage focus:
//   - happy-path list returns only this workspace's hooks
//   - enable / disable flip the column + emit a system.hook_toggled entry
//   - a hook ID that belongs to a sibling workspace 404s (cross-tenant
//     write is blocked)
//   - non-admin roles get 403 on enable/disable

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
)

// recordingEmitter captures journal.Emit calls so tests can assert the
// handler emitted the expected entries without standing up the full
// batched writer.
type recordingEmitter struct {
	entries []journal.Entry
}

func (e *recordingEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.entries = append(e.entries, entry)
	return "rec_" + entry.Summary, nil
}
func (e *recordingEmitter) Flush(_ context.Context) error { return nil }

// seedHook writes a hooks_config row with enabled=1 and a minimal
// http handler. Returns the hook ID.
func seedHook(t *testing.T, db *sql.DB, wsID, crewID string) string {
	t.Helper()
	h := hooks.Hook{
		WorkspaceID: wsID,
		CrewID:      crewID,
		Event:       hooks.EventPostToolCall,
		HandlerKind: hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url": "http://example.test/hook",
		},
		Enabled:  true,
		Blocking: false,
	}
	id, err := hooks.Register(context.Background(), db, h, false)
	if err != nil {
		t.Fatalf("register hook: %v", err)
	}
	return id
}

func TestHooksList_WorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Hook in this workspace — should appear.
	_ = seedHook(t, db, wsID, "")

	// Seed a second workspace + hook; must NOT appear in our caller's list.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	_ = seedHook(t, db, otherWS, "")

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/hooks", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows  []hookRow `json:"rows"`
		Count int       `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Rows) != 1 {
		t.Fatalf("expected 1 row for caller workspace, got %d (rows=%d)", resp.Count, len(resp.Rows))
	}
	if resp.Rows[0].WorkspaceID != wsID {
		t.Errorf("row workspace = %q, want %q", resp.Rows[0].WorkspaceID, wsID)
	}
}

func TestHooksEnable_Disable_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	hookID := seedHook(t, db, wsID, "")

	// Start enabled; disable it.
	rec := &recordingEmitter{}
	h := NewHooksHandler(db, newTestLogger())
	h.SetJournal(rec)

	req := httptest.NewRequest("POST", "/api/v1/hooks/"+hookID+"/disable", nil)
	req.SetPathValue("id", hookID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Disable(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Confirm the DB flipped.
	var enabledInt int
	if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id = ?`, hookID).Scan(&enabledInt); err != nil {
		t.Fatalf("query enabled: %v", err)
	}
	if enabledInt != 0 {
		t.Errorf("enabled = %d after disable, want 0", enabledInt)
	}

	// Journal entry recorded.
	if len(rec.entries) != 1 {
		t.Fatalf("expected 1 journal emit, got %d", len(rec.entries))
	}
	if rec.entries[0].Type != journal.EntrySystemHookToggled {
		t.Errorf("entry type = %q, want %q", rec.entries[0].Type, journal.EntrySystemHookToggled)
	}

	// Re-enable.
	req2 := httptest.NewRequest("POST", "/api/v1/hooks/"+hookID+"/enable", nil)
	req2.SetPathValue("id", hookID)
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.Enable(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("enable status = %d", rr2.Code)
	}
	if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id = ?`, hookID).Scan(&enabledInt); err != nil {
		t.Fatalf("re-query enabled: %v", err)
	}
	if enabledInt != 1 {
		t.Errorf("enabled = %d after enable, want 1", enabledInt)
	}
}

func TestHooksEnable_ForbiddenForMember(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	hookID := seedHook(t, db, wsID, "")

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/hooks/"+hookID+"/enable", nil)
	req.SetPathValue("id", hookID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Enable(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (member can't toggle)", rr.Code, http.StatusForbidden)
	}
}

func TestHooksEnable_CrossTenantIDReturns404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Create a second workspace + hook.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreignID := seedHook(t, db, otherWS, "")

	// Caller from wsID tries to toggle a hook owned by otherWS.
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/hooks/"+foreignID+"/enable", nil)
	req.SetPathValue("id", foreignID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Enable(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant enable status = %d, want 404 (no existence leak)", rr.Code)
	}

	// The foreign hook's enabled column must be unchanged.
	var enabledInt int
	_ = db.QueryRow(`SELECT enabled FROM hooks_config WHERE id = ?`, foreignID).Scan(&enabledInt)
	if enabledInt != 1 {
		t.Errorf("foreign hook enabled = %d, want 1 (untouched by cross-tenant call)", enabledInt)
	}
}
