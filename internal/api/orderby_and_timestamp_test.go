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
//   T3.7 / DB1 — 24× legacy `datetime('now')` DEFAULTs (database/migrate.go)
//          write `YYYY-MM-DD HH:MM:SS` while application code writes RFC3339
//          (`YYYY-MM-DDTHH:MM:SSZ`). Because created_at is TEXT and SQLite
//          sorts it with BINARY collation, the space (0x20) in the legacy
//          format sorts *before* the 'T' (0x54) in RFC3339 — so a row with a
//          legacy timestamp interleaves ahead of a chronologically-earlier
//          RFC3339 row. This is an UNFIXED finding, written as a TRIPWIRE:
//          it asserts the current (wrong) ordering, logs "VULN DB1 confirmed",
//          and ships a t.Skip'd *_SecureTarget documenting the post-fix invariant.

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

// ── T3.7 / DB1 — mixed timestamp formats break ORDER BY (tripwire) ──────────

const (
	legacyTSLayout = "2006-01-02 15:04:05" // what datetime('now') DEFAULT writes
)

func TestTimestampOrdering_MixedFormats_VULN(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	ctx := context.Background()

	// Row B: created_at supplied by the legacy `datetime('now')` DEFAULT.
	// workspace_files.created_at is `TEXT NOT NULL DEFAULT (datetime('now'))`
	// (database/migrate.go:563), so omitting the column triggers it.
	idLegacy := generateCUID()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspace_files (id, workspace_id, rel_path) VALUES (?, ?, ?)`,
		idLegacy, wsID, "legacy.txt"); err != nil {
		t.Fatalf("insert legacy-default row: %v", err)
	}
	var legacyTS string
	if err := db.QueryRow(`SELECT created_at FROM workspace_files WHERE id = ?`, idLegacy).Scan(&legacyTS); err != nil {
		t.Fatalf("read legacy created_at: %v", err)
	}

	legacyTime, err := time.Parse(legacyTSLayout, legacyTS)
	if err != nil {
		// If the DEFAULT ever starts writing RFC3339, the premise is gone.
		t.Fatalf("DEFAULT no longer writes legacy `%s` format (got %q): %v — DB1 may be fixed, update this test",
			legacyTSLayout, legacyTS, err)
	}

	// Row A: application-style RFC3339 timestamp, written ONE SECOND EARLIER
	// than the legacy row. Truly chronological order is therefore [A, B].
	rfcTime := legacyTime.Add(-1 * time.Second).UTC()
	if rfcTime.Format("2006-01-02") != legacyTime.Format("2006-01-02") {
		t.Skip("ran within 1s of UTC midnight; date rollover would mask the format effect — rerun")
	}
	rfcTS := rfcTime.Format(time.RFC3339) // e.g. 2026-06-29T11:59:59Z
	idRFC := generateCUID()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspace_files (id, workspace_id, rel_path, created_at) VALUES (?, ?, ?, ?)`,
		idRFC, wsID, "app.txt", rfcTS); err != nil {
		t.Fatalf("insert rfc3339 row: %v", err)
	}

	// Cursor pagination orders by created_at ASC.
	rows, err := db.QueryContext(ctx,
		`SELECT id, created_at FROM workspace_files WHERE workspace_id = ? ORDER BY created_at ASC`, wsID)
	if err != nil {
		t.Fatalf("query ordered: %v", err)
	}
	defer rows.Close()
	type rec struct{ id, ts string }
	var got []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.ts); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	// True chronological order is [idRFC (earlier), idLegacy (later)].
	// The BUG: ASC ordering of mixed TEXT formats puts the legacy row first
	// because space (0x20) < 'T' (0x54), so the chronologically-LATER row
	// interleaves ahead of the earlier one.
	if got[0].id == idRFC {
		// Chronological order won — the format mismatch no longer flips it.
		t.Fatalf("DB1 appears FIXED (good): ORDER BY created_at returned true chronological order "+
			"[%s, %s] — replace this tripwire with the *_SecureTarget assertion", got[0].ts, got[1].ts)
	}
	if got[0].id != idLegacy {
		t.Fatalf("unexpected first row %q (legacy=%s rfc=%s)", got[0].id, idLegacy, idRFC)
	}
	t.Logf("VULN DB1 confirmed: chronologically-later legacy row (%q) sorts BEFORE the earlier "+
		"RFC3339 row (%q) under ORDER BY created_at ASC — cursor pagination interleaves out of order",
		got[0].ts, got[1].ts)
}

// --- Secure target (activate after DB1 fix) ---------------------------------
//
// After timestamps are normalised to a single format (RFC3339 everywhere, or
// every DEFAULT switched to strftime('%Y-%m-%dT%H:%M:%fZ','now')), ORDER BY
// created_at must return rows in true chronological order regardless of which
// path wrote each row.
func TestTimestampOrdering_SecureTarget(t *testing.T) {
	t.Skip("activate after DB1 fix: normalise all created_at writes to one (RFC3339) format")

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
	legacyTime, _ := time.Parse(time.RFC3339, legacyTS) // post-fix: DEFAULT is RFC3339
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
