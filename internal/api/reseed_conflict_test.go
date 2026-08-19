package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The demo seed is written to be idempotent: cmd_seed_data.go treats a 409
// Conflict as "this row is already here" and keeps going. That contract only
// holds if the server actually says 409 when a UNIQUE constraint rejects the
// insert. Project and label create both mapped the constraint error onto
// internalError, so a re-seed against an already-seeded install died on an
// opaque 500 at the first duplicate — which is exactly what `./dev.sh seed`
// did on dev2 ("project Launch Prep: HTTP 500"), with the real reason visible
// only in the server log:
//
//	insert project: UNIQUE constraint failed: projects.workspace_id, projects.slug
//
// A duplicate is a client-visible conflict, not an internal error, and the
// seed cannot distinguish the two through the status code alone.

func TestProjectCreate_DuplicateSlugIsConflict(t *testing.T) {
	h, userID, wsID := covProjHandler(t)

	create := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects",
			jsonBody(map[string]any{"name": "Launch Prep", "status": "in_progress", "priority": "high"}))
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		return rec
	}

	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", rec.Code)
	}
	rec := create()
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	// The conflict must not have written a second row.
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND slug = 'launch-prep'`, wsID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

// Two different names can slugify to the same slug ("Launch Prep" and
// "launch-prep"). That collides on the same UNIQUE constraint and must read as
// a conflict too, not a 500 — the name being distinct does not make the row
// insertable.
func TestProjectCreate_DistinctNameSameSlugIsConflict(t *testing.T) {
	h, userID, wsID := covProjHandler(t)

	create := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{"name": name}))
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		return rec
	}

	if rec := create("Launch Prep"); rec.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", rec.Code)
	}
	if rec := create("launch-prep"); rec.Code != http.StatusConflict {
		t.Fatalf("slug-collision create: status = %d, want 409", rec.Code)
	}
}

// A duplicate slug in *another* workspace is not a duplicate at all — the
// constraint is (workspace_id, slug). Guards against a fix that maps every
// insert error to 409 and starts refusing legitimate rows.
func TestProjectCreate_SameSlugOtherWorkspaceSucceeds(t *testing.T) {
	h, userID, wsID := covProjHandler(t)

	// seedTestWorkspace hardcodes one id/slug, so the second tenant is seeded
	// inline rather than by calling it twice.
	otherWS := "reseed-other-workspace"
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', ?, ?, 'OWNER')`,
		otherWS, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	create := func(ws string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{"name": "Launch Prep"}))
		req = withWorkspaceUser(req, userID, ws, "OWNER")
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		return rec
	}

	if rec := create(wsID); rec.Code != http.StatusCreated {
		t.Fatalf("first workspace: status = %d, want 201", rec.Code)
	}
	if rec := create(otherWS); rec.Code != http.StatusCreated {
		t.Fatalf("second workspace: status = %d, want 201 — the constraint is per workspace", rec.Code)
	}
}

func TestCreateLabel_DuplicateNameIsConflict(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	create := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/labels",
			jsonBody(map[string]any{"name": "Bug", "color": "#EF4444"}))
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.CreateLabel(rec, req)
		return rec
	}

	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", rec.Code)
	}
	if rec := create(); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM labels WHERE workspace_id = ? AND name = 'Bug'`, wsID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}
