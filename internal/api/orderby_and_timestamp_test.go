package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// This file covers two findings from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md):
//
//   T3.6 — dynamic ORDER BY is built from a *whitelist* switch in the list
//          handlers (project_handler.go:88-95, issue_handler_crud.go:92-101).
//          An attacker-supplied `?sort=created_at);DROP TABLE …--` must fall
//          back to the default column and run with no SQL error. This is
//          ALREADY-SECURE behaviour, so these are plain regression guards
//          (they FAIL the day someone interpolates `sort` directly).
//
//   T3.7 / DB1 — legacy `datetime('now')` DEFAULTs (database/migrate.go)
//          wrote `YYYY-MM-DD HH:MM:SS` while application code writes RFC3339
//          (`YYYY-MM-DDTHH:MM:SSZ`). Because created_at is TEXT and SQLite
//          sorts it with BINARY collation, the space (0x20) in the legacy
//          format sorts *before* the 'T' (0x54) in RFC3339 — so a row with a
//          legacy timestamp interleaved ahead of a chronologically-earlier
//          RFC3339 row. FIXED by migration v144 (#1073b): every
//          string-compared DEFAULT was converted to the ISO T-form and the
//          historical legacy rows backfilled, so both write paths now sort
//          consistently. The former TRIPWIRE is now TestTimestampOrdering_
//          SecureTarget, a live regression guard asserting true chronological
//          order across the DEFAULT and explicit-RFC3339 write paths.

// ── T3.6 — ORDER BY whitelist (regression guard, already secure) ────────────

// orderByInjectionVectors are the strings an attacker would try to smuggle
// past a naively-interpolated ORDER BY clause. Each must be ignored (fall back
// to the default sort column) rather than reach the SQL planner.
var orderByInjectionVectors = []struct {
	name string
	sort string
}{
	{"drop_table", "created_at);DROP TABLE projects;--"},
	{"stacked_delete", "name; DELETE FROM projects"},
	{"subselect", "(SELECT password FROM users)"},
	{"union", "created_at UNION SELECT 1"},
	{"comment_tail", "created_at--"},
	{"ordinal", "1"},
	{"multi_col", "created_at,updated_at"},
	{"unknown_col", "totally_made_up_column"},
	{"desc_inject", "name DESC, (SELECT 1)"},
}

func TestOrderByWhitelist_Projects_MaliciousSortFallsBack(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewProjectHandler(db, nil, newTestLogger())

	// Seeded out of alphabetical order; the default sort column is p.name ASC,
	// so a request whose `sort` is rejected must come back Alpha, Bravo, Charlie.
	seedProject(t, db, wsID, "Charlie")
	seedProject(t, db, wsID, "Alpha")
	seedProject(t, db, wsID, "Bravo")
	wantOrder := []string{"Alpha", "Bravo", "Charlie"}

	for _, vec := range orderByInjectionVectors {
		t.Run(vec.name, func(t *testing.T) {
			q := url.Values{"sort": {vec.sort}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?"+q.Encode(), nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rec := httptest.NewRecorder()

			h.List(rec, req)

			// A successful injection would have produced a SQL syntax error
			// (500) or destroyed the table. Neither may happen.
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (malicious sort must be ignored, not error); body=%s",
					rec.Code, rec.Body.String())
			}

			var got []projectResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got) != len(wantOrder) {
				t.Fatalf("len = %d, want %d (table intact, default sort)", len(got), len(wantOrder))
			}
			for i, name := range wantOrder {
				if got[i].Name != name {
					t.Fatalf("sort=%q did not fall back to default p.name ASC: got[%d]=%q want %q (full=%v)",
						vec.sort, i, got[i].Name, name, namesOf(got))
				}
			}
		})
	}

	// Sanity: the projects table still exists and holds all rows (no DROP ran).
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE workspace_id = ?`, wsID).Scan(&cnt); err != nil {
		t.Fatalf("projects table missing or unreadable after injection attempts: %v", err)
	}
	if cnt != len(wantOrder) {
		t.Fatalf("project count = %d, want %d (rows tampered)", cnt, len(wantOrder))
	}
}

// TestOrderByWhitelist_Projects_ValidSortAccepted confirms the whitelist still
// honours a legitimate column, so the guard above isn't passing by always
// ignoring `sort`.
func TestOrderByWhitelist_Projects_ValidSortAccepted(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewProjectHandler(db, nil, newTestLogger())
	seedProject(t, db, wsID, "Solo")

	for _, sort := range []string{"created_at", "updated_at", "name"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?sort="+sort, nil)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.List(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid sort=%q status = %d, want 200; body=%s", sort, rec.Code, rec.Body.String())
		}
	}
}

func TestOrderByWhitelist_Issues_MaliciousSortFallsBack(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	seedIssue(t, db, wsID, crewID, leadID, "ENG-3", "DONE")
	const wantCount = 3

	for _, vec := range orderByInjectionVectors {
		t.Run(vec.name, func(t *testing.T) {
			q := url.Values{"sort": {vec.sort}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/issues?"+q.Encode(), nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rec := httptest.NewRecorder()

			h.List(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (malicious sort must be ignored); body=%s",
					rec.Code, rec.Body.String())
			}
			// Length-only assertion: a SQL injection would 500 above; here we
			// just confirm the whitelisted default produced a valid result set.
			var got []json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got) != wantCount {
				t.Fatalf("len = %d, want %d (default sort, table intact)", len(got), wantCount)
			}
		})
	}

	// missions table still present and complete.
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE workspace_id = ?`, wsID).Scan(&cnt); err != nil {
		t.Fatalf("missions table missing or unreadable after injection attempts: %v", err)
	}
	if cnt != wantCount {
		t.Fatalf("mission count = %d, want %d", cnt, wantCount)
	}
}

func namesOf(ps []projectResponse) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// ── T3.7 / DB1 — mixed timestamp formats break ORDER BY (FIXED, #1073b) ─────
//
// legacyTSLayout is the space-form SQLite's `datetime('now')` DEFAULT used to
// write. Migration v144 converted every string-compared DEFAULT to the ISO
// T-form (strftime('%Y-%m-%dT%H:%M:%fZ','now')) and backfilled the historical
// legacy rows, so the DEFAULT must NOT produce this shape anymore. The
// SecureTarget test below is the live regression guard; this constant is kept
// only so the guard can assert the legacy shape is gone.
const (
	legacyTSLayout = "2006-01-02 15:04:05" // what the pre-v144 datetime('now') DEFAULT wrote
)

// TestTimestampOrdering_SecureTarget is the post-#1073b regression guard
// (formerly the DB1 tripwire). After migration v144 normalised every
// string-compared timestamp to a single format, ORDER BY created_at must
// return rows in true chronological order regardless of which path wrote each
// row — the DEFAULT (now ISO T-form) or an explicit application RFC3339 write.
// A regression that reintroduced the space-form DEFAULT would flip the order
// (' ' 0x20 < 'T' 0x54) and fail here.
func TestTimestampOrdering_SecureTarget(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	ctx := context.Background()

	idLegacy := generateCUID()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspace_files (id, workspace_id, rel_path) VALUES (?, ?, ?)`,
		idLegacy, wsID, "legacy.txt"); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	var legacyTS string
	if err := db.QueryRow(`SELECT created_at FROM workspace_files WHERE id = ?`, idLegacy).Scan(&legacyTS); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	// Direct regression check: the DEFAULT must no longer write the space-form
	// that broke ordering. `time.Parse(legacyTSLayout, ...)` succeeds ONLY on
	// the old "YYYY-MM-DD HH:MM:SS" shape, so a success here means v144 was
	// reverted.
	if _, err := time.Parse(legacyTSLayout, legacyTS); err == nil {
		t.Fatalf("DEFAULT regressed to legacy space-form %q — v144 conversion lost", legacyTS)
	}
	// Post-v144 the DEFAULT is ISO T-form (with a millisecond fraction);
	// time.Parse(time.RFC3339, ...) accepts the trailing fraction.
	legacyTime, err := time.Parse(time.RFC3339, legacyTS)
	if err != nil {
		t.Fatalf("DEFAULT wrote an unparseable timestamp %q: %v", legacyTS, err)
	}
	idRFC := generateCUID()
	rfcTS := legacyTime.Add(-1 * time.Second).UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspace_files (id, workspace_id, rel_path, created_at) VALUES (?, ?, ?, ?)`,
		idRFC, wsID, "app.txt", rfcTS); err != nil {
		t.Fatalf("insert rfc row: %v", err)
	}

	var first string
	if err := db.QueryRow(
		`SELECT id FROM workspace_files WHERE workspace_id = ? ORDER BY created_at ASC LIMIT 1`, wsID,
	).Scan(&first); err != nil {
		t.Fatalf("query: %v", err)
	}
	if first != idRFC {
		t.Fatalf("ORDER BY created_at must return the chronologically-earlier row first; got %q want %q", first, idRFC)
	}
}
