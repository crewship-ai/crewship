package api

// PATCH /api/v1/labels/{labelId} was a hard 500 for every caller, in every
// workspace, since the endpoint shipped: UpdateLabel built its statement with
// newUpdate(), which always emits "updated_at = ?" first, and the labels table
// has only created_at. SQLite answered "no such column: updated_at" and the
// handler mapped that to a 500.
//
// It surfaced during the cross-workspace fence work rather than from a bug
// report, and the reason is worth recording: a route that 500s cannot be tested
// for tenancy. The fence probe against it was neither red nor green, it was
// meaningless — the UPDATE never executed, so nothing could be said about its
// WHERE clause. Fixing the 500 is what made the fence assertion real.
//
// This test pins both halves: the rename works in the caller's own workspace,
// and a label id from another workspace is a 404 with the row untouched.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateLabel_RenamesInOwnWorkspaceAndFencesOthers(t *testing.T) {
	ensureEncryptionKey(t)
	ctx := t.Context()
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "a")
	victim := fenceSeedTenant(t, db, "b")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	patch := func(ten *fenceTenant, labelID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/v1/labels/"+labelID+"?workspace_id="+ten.wsID,
			strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ten.token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	// Own workspace: the rename must actually work. Before the fix this was a
	// 500 for everyone.
	rr := patch(attacker, attacker.ids["labelId"], `{"name":"renamed","color":"#123456"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH own label = %d, want 200; body=%s", rr.Code, fenceTrim(rr.Body.String()))
	}
	var name, color string
	if err := db.QueryRowContext(ctx, `SELECT name, color FROM labels WHERE id = ?`, attacker.ids["labelId"]).
		Scan(&name, &color); err != nil {
		t.Fatalf("read label back: %v", err)
	}
	if name != "renamed" || color != "#123456" {
		t.Errorf("label after PATCH = (%q, %q), want (renamed, #123456)", name, color)
	}

	// Other workspace: 404, and the victim's row is untouched.
	rr = patch(attacker, victim.ids["labelId"], `{"name":"pwned"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH cross-workspace label = %d, want 404; body=%s", rr.Code, fenceTrim(rr.Body.String()))
	}
	if err := db.QueryRowContext(ctx, `SELECT name FROM labels WHERE id = ?`, victim.ids["labelId"]).Scan(&name); err != nil {
		t.Fatalf("read victim label: %v", err)
	}
	if name != victim.marker("labels") {
		t.Errorf("victim label name = %q, want it unchanged (%q)", name, victim.marker("labels"))
	}
}
