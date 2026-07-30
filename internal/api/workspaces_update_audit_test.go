package api

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// The audit trail is what answers "when did this setting move, and who
// moved it". A PATCH that persists nothing is not a setting moving, and
// recording one puts an event in that trail for something that never
// happened — the dashboard sends `{}` on any save where the form was
// opened and closed unchanged, so this is not a hypothetical body.
func TestWorkspaceUpdate_AuditsOnlyWhatWasPersisted(t *testing.T) {
	countUpdates := func(t *testing.T, h *WorkspaceHandler, wsID string) int {
		t.Helper()
		var n int
		if err := h.db.QueryRow(
			`SELECT COUNT(*) FROM audit_logs WHERE workspace_id = ? AND action = 'workspace.update'`,
			wsID).Scan(&n); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return n
	}

	patch := func(t *testing.T, h *WorkspaceHandler, wsID, userID, body string) {
		t.Helper()
		req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID, strings.NewReader(body))
		req.SetPathValue("workspaceId", wsID)
		ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
		ctx = context.WithValue(ctx, ctxRole, "OWNER")
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		h.Update(w, req)
		if w.Code != 200 {
			t.Fatalf("PATCH %s = %d, body=%s", body, w.Code, w.Body.String())
		}
	}

	t.Run("a body that changes nothing records nothing", func(t *testing.T) {
		db := setupTestDB(t)
		h := &WorkspaceHandler{db: db, logger: slog.Default()}
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)

		patch(t, h, wsID, userID, `{}`)

		if n := countUpdates(t, h, wsID); n != 0 {
			t.Errorf("a no-op PATCH wrote %d workspace.update rows, want 0", n)
		}
	})

	t.Run("a body that changes a field still records it", func(t *testing.T) {
		db := setupTestDB(t)
		h := &WorkspaceHandler{db: db, logger: slog.Default()}
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)

		patch(t, h, wsID, userID, `{"name":"Renamed"}`)

		if n := countUpdates(t, h, wsID); n != 1 {
			t.Errorf("a real rename wrote %d workspace.update rows, want 1", n)
		}
	})
}
