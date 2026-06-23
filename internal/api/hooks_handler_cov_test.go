package api

// Coverage tests for hooks_handler.go: SetJournal(nil) fallback, the
// List scan-error 500, malformed matcher / handler_config JSON
// tolerance, the legacy "2006-01-02 15:04:05" timestamp fallback, and
// round-tripping a real matcher through Register → List.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
)

func TestHooksSetJournal_NilCollapsesToNoop(t *testing.T) {
	db := setupTestDB(t)
	h := NewHooksHandler(db, newTestLogger())

	h.SetJournal(&recordingEmitter{})
	if _, ok := h.journal.(*recordingEmitter); !ok {
		t.Fatalf("journal = %T, want *recordingEmitter after SetJournal", h.journal)
	}
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("journal = %T, want noopEmitter after SetJournal(nil)", h.journal)
	}
}

func TestHooksList_ScanError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// blocking holds TEXT — Scan into int fails and the handler must
	// return the scan-failure 500 rather than rendering garbage.
	if _, err := db.Exec(`INSERT INTO hooks_config (id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled)
		VALUES ('hk-corrupt', ?, 'post_tool_call', '{}', 'http', '{}', 'not-an-int', 1)`, wsID); err != nil {
		t.Fatalf("seed corrupt hook: %v", err)
	}

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/hooks", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "scan failed") {
		t.Errorf("body = %s, want 'scan failed'", rr.Body.String())
	}
}

func TestHooksList_MalformedJSONAndLegacyTimestamps(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Row with unparseable matcher + handler_config and legacy
	// "datetime('now')"-style timestamps. The row must still render
	// with zero-value Matcher / empty HandlerConfig and the fallback
	// timestamp layout parsed.
	if _, err := db.Exec(`INSERT INTO hooks_config (id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled, created_at, updated_at)
		VALUES ('hk-legacy', ?, 'post_tool_call', 'not json', 'http', 'also not json', 1, 1, '2026-01-02 15:04:05', '2026-01-03 16:05:06')`, wsID); err != nil {
		t.Fatalf("seed legacy hook: %v", err)
	}

	// Row registered through the real store with a non-empty matcher —
	// the happy unmarshal path must round-trip the matcher fields.
	matched := hooks.Hook{
		WorkspaceID: wsID,
		Event:       hooks.EventPostToolCall,
		HandlerKind: hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url": "http://example.test/hook",
		},
		Matcher: hooks.Matcher{Tools: []string{"Bash"}},
		Enabled: true,
	}
	matchedID, err := hooks.Register(context.Background(), db, matched, false)
	if err != nil {
		t.Fatalf("register hook: %v", err)
	}

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/hooks", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows  []hookRow `json:"rows"`
		Count int       `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Fatalf("count = %d, want 2", resp.Count)
	}

	byID := map[string]hookRow{}
	for _, row := range resp.Rows {
		byID[row.ID] = row
	}

	legacy, ok := byID["hk-legacy"]
	if !ok {
		t.Fatal("hk-legacy missing from list (malformed JSON must not drop the row)")
	}
	if len(legacy.Matcher.Tools) != 0 {
		t.Errorf("legacy matcher = %+v, want zero value on malformed JSON", legacy.Matcher)
	}
	if len(legacy.HandlerConfig) != 0 {
		t.Errorf("legacy handler_config = %v, want empty map on malformed JSON", legacy.HandlerConfig)
	}
	wantCreated := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if !legacy.CreatedAt.Equal(wantCreated) {
		t.Errorf("legacy CreatedAt = %v, want %v (fallback layout)", legacy.CreatedAt, wantCreated)
	}
	wantUpdated := time.Date(2026, 1, 3, 16, 5, 6, 0, time.UTC)
	if !legacy.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("legacy UpdatedAt = %v, want %v (fallback layout)", legacy.UpdatedAt, wantUpdated)
	}
	if !legacy.Blocking {
		t.Error("legacy blocking = false, want true")
	}

	withMatcher, ok := byID[matchedID]
	if !ok {
		t.Fatalf("registered hook %s missing from list", matchedID)
	}
	if len(withMatcher.Matcher.Tools) != 1 || withMatcher.Matcher.Tools[0] != "Bash" {
		t.Errorf("matcher tools = %v, want [Bash]", withMatcher.Matcher.Tools)
	}
	if withMatcher.HandlerConfig["url"] != "http://example.test/hook" {
		t.Errorf("handler_config url = %v, want http://example.test/hook", withMatcher.HandlerConfig["url"])
	}
}
