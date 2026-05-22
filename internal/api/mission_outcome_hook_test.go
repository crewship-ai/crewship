package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitForCrewLesson polls until the crew lessons.md file appears
// AND contains the supplied "mustContain" substring, or the deadline
// expires. The mission-outcome hook fires asynchronously in a detached
// goroutine (emitMissionOutcomeLessonAsync), so a fixed time.Sleep
// would either be too short (flake under load) or too long (slow
// tests). Polling at 25ms intervals keeps the happy path well under
// 100ms while giving a slow CI runner up to 3s before failing.
//
// The mustContain argument lets callers wait for a SPECIFIC write
// (e.g. "kind: neutral" after a retransition). Without it, a test
// that fires two writes could race: see lessons.md created by the
// first write, assert on stale content, then a still-in-flight
// second write leaves stray .lock/.tmp files that break t.Cleanup's
// RemoveAll with "directory not empty".
func waitForCrewLesson(t *testing.T, storagePath, crewID, mustContain string) string {
	t.Helper()
	path := filepath.Join(storagePath, "crews", crewID, "shared", ".memory", "lessons.md")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			body := string(data)
			if mustContain == "" || strings.Contains(body, mustContain) {
				return body
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("crew lessons.md @ %s did not contain %q within 3s", path, mustContain)
	return ""
}

// TestMissionUpdate_TerminalTransitionEmitsCrewLesson verifies the
// end-to-end F4.5 hook: a PATCH that transitions a mission from
// REVIEW → COMPLETED must
//
//   - return HTTP 200 to the operator (status change succeeds even
//     if the hook were to fail)
//   - asynchronously write a single entry to
//     <storagePath>/crews/{crew_id}/shared/.memory/lessons.md
//   - record source=mission_outcome and kind=positive
//
// The hook runs in a detached goroutine; the test polls for the file
// rather than asserting it exists immediately.
func TestMissionUpdate_TerminalTransitionEmitsCrewLesson(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	storagePath := t.TempDir()

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	// Seed a mission in REVIEW so REVIEW→COMPLETED is a valid transition.
	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, identifier, status, created_at, updated_at)
		VALUES ('m_outcome_ok', ?, ?, ?, 'trace_outcome_ok', 'Build feature X', 'ENG-42', 'REVIEW', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, nil, logger)
	handler.SetStoragePath(storagePath)

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m_outcome_ok", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m_outcome_ok")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body2 := waitForCrewLesson(t, storagePath, crewID, "id: mission_outcome_m_outcome_ok")
	for _, want := range []string{
		"id: mission_outcome_m_outcome_ok",
		"kind: positive",
		"source: mission_outcome",
		"ENG-42",
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("crew lessons.md missing %q; got:\n%s", want, body2)
		}
	}
}

// TestMissionUpdate_TerminalRetransitionIsIdempotent — re-PATCHing
// from one terminal state to another (FAILED → CANCELLED) is allowed
// by the transitions table; the hook must NOT duplicate the lessons
// entry. The lesson ID is anchored to the mission, not the status,
// so the second write should replace-in-place rather than append.
func TestMissionUpdate_TerminalRetransitionIsIdempotent(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	storagePath := t.TempDir()

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-2", "LEAD")

	// Direct insert at IN_PROGRESS so IN_PROGRESS→FAILED is allowed.
	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, identifier, status, created_at, updated_at)
		VALUES ('m_outcome_idem', ?, ?, ?, 'trace_outcome_idem', 'Flaky job', 'DEV-9', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, nil, logger)
	handler.SetStoragePath(storagePath)

	doPatch := func(newStatus string) {
		body := bytes.NewBufferString(`{"status":"` + newStatus + `"}`)
		req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m_outcome_idem", body)
		req.SetPathValue("crewId", crewID)
		req.SetPathValue("missionId", "m_outcome_idem")
		ctx := withUser(req.Context(), &AuthUser{ID: userID})
		ctx = withWorkspace(ctx, wsID, "MANAGER")
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.Update(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch to %s: status=%d body=%s", newStatus, rr.Code, rr.Body.String())
		}
	}

	doPatch("FAILED")
	// Wait for the FIRST write to land with the negative kind.
	waitForCrewLesson(t, storagePath, crewID, "kind: negative")

	// FAILED is not in the transitions table outbound — but the issue-
	// tracker statuses do allow FAILED→BACKLOG→TODO→IN_PROGRESS→…
	// For a deterministic second terminal transition, re-create:
	if _, err := db.Exec(`UPDATE missions SET status = 'IN_PROGRESS' WHERE id = 'm_outcome_idem'`); err != nil {
		t.Fatalf("reset status: %v", err)
	}
	doPatch("CANCELLED")

	// Wait for the SECOND write to flip kind to neutral (replace-in-
	// place). Polling for the specific terminal-state marker guarantees
	// both goroutines have settled before assertions + t.Cleanup; an
	// existence-only poll could see the first write and return while
	// the second write is still racing, leaking .lock files into the
	// temp dir and breaking RemoveAll.
	body := waitForCrewLesson(t, storagePath, crewID, "kind: neutral")
	count := strings.Count(body, "id: mission_outcome_m_outcome_idem")
	if count != 1 {
		t.Errorf("expected exactly 1 entry for the same mission across retransitions, got %d:\n%s", count, body)
	}
}

// TestMissionUpdate_NonTerminalTransitionEmitsNoLesson — IN_PROGRESS
// is a valid PLANNING outbound transition but is NOT terminal. The
// hook must NOT write any file. This is the test that catches a
// regression where someone removes the `terminal` gate in the helper
// and lessons.md starts filling with PLANNING→IN_PROGRESS row noise.
func TestMissionUpdate_NonTerminalTransitionEmitsNoLesson(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	storagePath := t.TempDir()

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-3", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, identifier, status, created_at, updated_at)
		VALUES ('m_outcome_nt', ?, ?, ?, 'trace_outcome_nt', 'Begin work', 'ENG-7', 'PLANNING', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, nil, logger)
	handler.SetStoragePath(storagePath)

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m_outcome_nt", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m_outcome_nt")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Give the (non-existent) goroutine plenty of time. Cleanup pass:
	// after 200ms the file must still not be there.
	time.Sleep(200 * time.Millisecond)
	path := filepath.Join(storagePath, "crews", crewID, "shared", ".memory", "lessons.md")
	if _, err := os.Stat(path); err == nil {
		data, _ := os.ReadFile(path)
		t.Errorf("non-terminal transition should not write lessons.md; got:\n%s", string(data))
	}
}

// TestMissionUpdate_UnwiredStoragePath_StillSucceeds — when the
// router fails to call SetStoragePath (unit-test paths, partial
// upgrades), the status transition must still work cleanly. The hook
// silently no-ops. This pins the operator-friendliness contract:
// degraded F4.5 wiring never breaks the operator's PATCH call.
func TestMissionUpdate_UnwiredStoragePath_StillSucceeds(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-4", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m_outcome_unwired', ?, ?, ?, 'trace_outcome_unwired', 'Unwired test', 'REVIEW', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// Notice: no SetStoragePath call.
	handler := NewMissionHandler(db, nil, nil, logger)

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m_outcome_unwired", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m_outcome_unwired")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unwired storagePath should not affect API response; status=%d body=%s", rr.Code, rr.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["status"] != "COMPLETED" {
		t.Errorf("status = %v, want COMPLETED", result["status"])
	}
}

// TestEmitMissionOutcomeLessonAsync_MissingMissionRow — operator
// deletes a mission between the status update and the hook firing
// (race window is tiny but real). The hook must log + return cleanly,
// not panic or leak a goroutine. This is exercised through the helper
// directly because constructing the race in the integration path
// would be flaky.
func TestEmitMissionOutcomeLessonAsync_MissingMissionRow(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	storagePath := t.TempDir()

	// Don't insert any mission row.
	emitMissionOutcomeLessonAsync(
		context.Background(), db, storagePath,
		"ghost_mission_id", "COMPLETED", logger,
	)

	// Give the goroutine 200ms; nothing should land on disk because
	// the DB read fails.
	time.Sleep(200 * time.Millisecond)
	hits := 0
	_ = filepath.Walk(storagePath, func(path string, _ os.FileInfo, _ error) error {
		if strings.HasSuffix(path, "lessons.md") {
			hits++
		}
		return nil
	})
	if hits != 0 {
		t.Errorf("expected zero lessons.md files for ghost mission, got %d", hits)
	}
}

// TestTerminalStatusToLessonKindLocal_NormalizesCase verifies the
// case+whitespace normalization that CodeRabbit flagged in review.
// A future caller passing "completed" or " COMPLETED " must still
// be treated as terminal so the hook fires — without normalization
// these would silently skip.
func TestTerminalStatusToLessonKindLocal_NormalizesCase(t *testing.T) {
	cases := []struct {
		in          string
		wantKind    string
		wantTermina bool
	}{
		{"COMPLETED", "positive", true},
		{"completed", "positive", true},
		{" COMPLETED ", "positive", true},
		{"DONE", "positive", true},
		{"done", "positive", true},
		{"FAILED", "negative", true},
		{"failed", "negative", true},
		{"CANCELLED", "neutral", true},
		{"cancelled", "neutral", true},
		{"  CANCELLED\t", "neutral", true},
		{"IN_PROGRESS", "", false},
		{"in_progress", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			kind, terminal := terminalStatusToLessonKindLocal(tc.in)
			if terminal != tc.wantTermina {
				t.Errorf("terminal flag: got %v, want %v", terminal, tc.wantTermina)
			}
			if kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", kind, tc.wantKind)
			}
		})
	}
}

// TestParseStoredTimestamp_HandlesLegacyAndModernShapes pins the
// multi-layout parser. The mission row's completed_at can carry
// either modern RFC3339 (written by current code paths) or the
// SQLite datetime('now') shape ("2026-05-22 17:12:12") from older
// DEFAULT rows. Both must parse so the lesson's captured_at preserves
// the original transition time instead of silently rewriting to
// hook-execution time.
func TestParseStoredTimestamp_HandlesLegacyAndModernShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		valid bool
	}{
		{"rfc3339_utc", "2026-05-22T17:12:12Z", true},
		{"rfc3339_offset", "2026-05-22T19:12:12+02:00", true},
		{"rfc3339_nano", "2026-05-22T17:12:12.123456789Z", true},
		{"sqlite_default", "2026-05-22 17:12:12", true},
		{"sqlite_subsec", "2026-05-22 17:12:12.123", true},
		{"rfc3339_no_zone", "2026-05-22T17:12:12", true},
		{"trim_whitespace", "  2026-05-22T17:12:12Z\t", true},
		{"empty", "", false},
		{"garbage", "not a timestamp", false},
		{"date_only", "2026-05-22", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, ok := parseStoredTimestamp(tc.raw)
			if ok != tc.valid {
				t.Errorf("parse %q: got ok=%v, want %v", tc.raw, ok, tc.valid)
			}
			if ok && ts.Location() != time.UTC {
				t.Errorf("parse %q: result not in UTC: %v", tc.raw, ts.Location())
			}
		})
	}
}

// Ensure the test file actually uses the sql.DB import via setupTestDB
// in case the lint-cleanup pass removes "database/sql".
var _ = (*sql.DB)(nil)
