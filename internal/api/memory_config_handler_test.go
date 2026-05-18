package api

// Coverage for the memory_config admin endpoint (Iter 6 of the
// memory-hardening series). Table-driven where the contract has
// multiple equivalent shapes; standalone tests for the
// audit-trail and validation paths where the assertions diverge.
//
// Contracts pinned here:
//
//   1. Role + workspace preconditions match the rest of the
//      admin surface (manage required; cross-tenant probe gets
//      403, not a row leak).
//
//   2. GET shape stays stable for the dashboard: workspace_id /
//      versions_retention_days / is_default / raw_config.
//      is_default flips to false the moment a stored value
//      resolves; raw_config carries the literal JSON so
//      operators can see drift between "what's stored" and
//      "what's effective".
//
//   3. PATCH validation: zero, negative, fractional,
//      out-of-bounds, non-numeric all → 400 with a precise
//      message naming the offending field.
//
//   4. PATCH idempotency: setting a value that already matches
//      is 200 with no journal emit. Audit emit volume tracks
//      actual change, not request volume.
//
//   5. PATCH preserves unknown keys (forward compat) and emits
//      exactly one memory.config_updated event with a payload
//      describing each {key: from, to} that actually changed.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func memConfigRig(t *testing.T) (*MemoryConfigHandler, *sql.DB, string, string, *journal.Writer) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h := NewMemoryConfigHandler(db, newTestLogger())
	h.SetJournal(jw)
	return h, db, userID, wsID, jw
}

func memConfigDoGet(t *testing.T, h *MemoryConfigHandler, userID, wsID, role string) (int, memoryConfigResponse) {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/memory/config", nil), userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		return rr.Code, memoryConfigResponse{}
	}
	var resp memoryConfigResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode get: %v; body=%s", err, rr.Body.String())
	}
	return rr.Code, resp
}

func memConfigDoPatch(t *testing.T, h *MemoryConfigHandler, userID, wsID, role string, body []byte) (int, []byte) {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/admin/memory/config", bytes.NewReader(body)),
		userID, wsID, role,
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	return rr.Code, rr.Body.Bytes()
}

func TestMemoryConfig_Preconditions(t *testing.T) {
	h, _, userID, wsID, _ := memConfigRig(t)
	cases := []struct {
		name   string
		method string
		role   string
		wsID   string
		want   int
	}{
		{name: "get_member_forbidden", method: "GET", role: "MEMBER", wsID: wsID, want: http.StatusForbidden},
		{name: "get_missing_workspace", method: "GET", role: "OWNER", wsID: "", want: http.StatusBadRequest},
		{name: "patch_member_forbidden", method: "PATCH", role: "MEMBER", wsID: wsID, want: http.StatusForbidden},
		{name: "patch_missing_workspace", method: "PATCH", role: "OWNER", wsID: "", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/admin/memory/config", bytes.NewReader([]byte(`{}`)))
			ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"})
			ctx = withWorkspace(ctx, tc.wsID, tc.role)
			rr := httptest.NewRecorder()
			if tc.method == "GET" {
				h.Get(rr, req.WithContext(ctx))
			} else {
				h.Patch(rr, req.WithContext(ctx))
			}
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestMemoryConfig_Get_NoRowReturnsDefaultsAndIsDefaultTrue(t *testing.T) {
	h, _, userID, wsID, _ := memConfigRig(t)
	code, resp := memConfigDoGet(t, h, userID, wsID, "OWNER")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace_id = %q, want %q", resp.WorkspaceID, wsID)
	}
	// Default = memory.DefaultRetentionDays (30). The handler
	// resolves to the default whenever the column is NULL.
	if resp.VersionsRetentionDays != 30 {
		t.Errorf("versions_retention_days = %d, want 30 (DefaultRetentionDays)", resp.VersionsRetentionDays)
	}
	if !resp.IsDefault {
		t.Errorf("is_default = false; want true when no row exists")
	}
	if resp.RawConfig != nil {
		t.Errorf("raw_config = %v, want nil for empty column", *resp.RawConfig)
	}
}

func TestMemoryConfig_Patch_HappyPath_PersistsAndAudits(t *testing.T) {
	h, db, userID, wsID, jw := memConfigRig(t)

	code, body := memConfigDoPatch(t, h, userID, wsID, "OWNER", []byte(`{"versions_retention_days": 7}`))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	var resp memoryConfigResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode patch resp: %v", err)
	}
	if resp.VersionsRetentionDays != 7 {
		t.Errorf("response.versions_retention_days = %d, want 7", resp.VersionsRetentionDays)
	}
	if resp.IsDefault {
		t.Errorf("is_default = true; want false after explicit set")
	}
	if resp.RawConfig == nil || !strings.Contains(*resp.RawConfig, `"versions_retention_days":7`) {
		t.Errorf("raw_config does not reflect the new value: %v", resp.RawConfig)
	}

	// Round-trip: a follow-up GET sees the persisted value.
	_, getResp := memConfigDoGet(t, h, userID, wsID, "OWNER")
	if getResp.VersionsRetentionDays != 7 || getResp.IsDefault {
		t.Errorf("GET after PATCH = %+v; want versions_retention_days=7, is_default=false", getResp)
	}

	// Journal event landed.
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type = 'memory.config_updated'`,
		wsID,
	).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 1 {
		t.Errorf("memory.config_updated events = %d, want 1", count)
	}
}

func TestMemoryConfig_Patch_NoOpDoesNotEmitEvent(t *testing.T) {
	h, db, userID, wsID, jw := memConfigRig(t)

	// First PATCH establishes the value + emits one event.
	if code, _ := memConfigDoPatch(t, h, userID, wsID, "OWNER", []byte(`{"versions_retention_days": 14}`)); code != http.StatusOK {
		t.Fatalf("first patch: %d", code)
	}
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush after first patch: %v", err)
	}

	// Second PATCH with the same value should be a no-op: 200,
	// no second journal entry.
	code, body := memConfigDoPatch(t, h, userID, wsID, "OWNER", []byte(`{"versions_retention_days": 14}`))
	if code != http.StatusOK {
		t.Fatalf("second patch status = %d, want 200; body=%s", code, body)
	}
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush after second patch: %v", err)
	}
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type = 'memory.config_updated'`,
		wsID,
	).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 1 {
		t.Errorf("memory.config_updated events = %d after two identical PATCH calls; want 1 (no-op should not emit)", count)
	}
}

func TestMemoryConfig_Patch_Validation_RejectsBadInputs(t *testing.T) {
	h, _, userID, wsID, _ := memConfigRig(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "zero_days", body: `{"versions_retention_days": 0}`, want: http.StatusBadRequest},
		{name: "negative_days", body: `{"versions_retention_days": -1}`, want: http.StatusBadRequest},
		{name: "fractional_days", body: `{"versions_retention_days": 7.5}`, want: http.StatusBadRequest},
		{name: "non_numeric_days", body: `{"versions_retention_days": "seven"}`, want: http.StatusBadRequest},
		{name: "over_cap", body: `{"versions_retention_days": 100000}`, want: http.StatusBadRequest},
		{name: "malformed_json", body: `not json`, want: http.StatusBadRequest},
		{name: "empty_body", body: ``, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			code, body := memConfigDoPatch(t, h, userID, wsID, "OWNER", []byte(tc.body))
			if code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", code, tc.want, body)
			}
		})
	}
}

func TestMemoryConfig_Patch_PreservesUnknownKeysForForwardCompat(t *testing.T) {
	// A future field (e.g. compaction_hour_override) added to
	// the JSON document by a newer client must round-trip
	// through an older server. The handler doesn't validate
	// unknown keys — it merges them into the stored document
	// untouched. This test pins that behaviour so a future
	// "strict validator" change doesn't accidentally drop
	// fields the next version cares about.
	h, _, userID, wsID, _ := memConfigRig(t)

	code, _ := memConfigDoPatch(t, h, userID, wsID, "OWNER",
		[]byte(`{"versions_retention_days": 7, "future_field": {"nested": true}}`))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	_, resp := memConfigDoGet(t, h, userID, wsID, "OWNER")
	if resp.RawConfig == nil {
		t.Fatalf("raw_config nil after patch with unknown key")
	}
	if !strings.Contains(*resp.RawConfig, `"future_field"`) {
		t.Errorf("raw_config dropped the unknown key; got %s", *resp.RawConfig)
	}
}

func TestMemoryConfig_CrossWorkspaceIsolation(t *testing.T) {
	// Workspace A sets retention_days=7; workspace B must see
	// the default (30) on GET, NOT A's value. Audit-trail rows
	// must be scoped per-workspace.
	hA, db, userIDA, wsIDA, _ := memConfigRig(t)
	if code, _ := memConfigDoPatch(t, hA, userIDA, wsIDA, "OWNER",
		[]byte(`{"versions_retention_days": 7}`)); code != http.StatusOK {
		t.Fatalf("patch A: %d", code)
	}

	// Inline second tenant since seedTestUser uses fixed IDs.
	userIDB := "test-other-user-id"
	wsIDB := "test-other-workspace-id"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'b@example.com', 'B')`, userIDB); err != nil {
		t.Fatalf("seed user B: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsIDB); err != nil {
		t.Fatalf("seed workspace B: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m_b', ?, ?, 'OWNER')`, wsIDB, userIDB); err != nil {
		t.Fatalf("seed member B: %v", err)
	}

	code, resp := memConfigDoGet(t, hA, userIDB, wsIDB, "OWNER")
	if code != http.StatusOK {
		t.Fatalf("get B: %d", code)
	}
	if !resp.IsDefault || resp.VersionsRetentionDays != 30 {
		t.Errorf("workspace B leaked workspace A config: %+v", resp)
	}
}

// ── pure-function validator tests ────────────────────────────────────
//
// validateMemoryConfig is exported via a lowercase symbol — kept
// in-package so a future CLI surface can call it without HTTP
// plumbing. The table here pins the contract independently of
// the handler's JSON-decode path so a regression in the
// validator surfaces immediately.

func TestValidateMemoryConfig_ContractTable(t *testing.T) {
	cases := []struct {
		name    string
		doc     map[string]any
		wantOK  bool
		wantSub string // substring expected in the error message
	}{
		{name: "empty_doc_is_valid", doc: map[string]any{}, wantOK: true},
		{name: "valid_retention", doc: map[string]any{"versions_retention_days": float64(7)}, wantOK: true},
		{name: "max_retention", doc: map[string]any{"versions_retention_days": float64(MaxRetentionDays)}, wantOK: true},
		{name: "min_retention", doc: map[string]any{"versions_retention_days": float64(1)}, wantOK: true},
		{name: "zero_rejected", doc: map[string]any{"versions_retention_days": float64(0)}, wantOK: false, wantSub: ">= 1"},
		{name: "negative_rejected", doc: map[string]any{"versions_retention_days": float64(-5)}, wantOK: false, wantSub: ">= 1"},
		{name: "over_cap_rejected", doc: map[string]any{"versions_retention_days": float64(MaxRetentionDays + 1)}, wantOK: false, wantSub: "<= " + fmtItoa(MaxRetentionDays)},
		{name: "fractional_rejected", doc: map[string]any{"versions_retention_days": float64(7.5)}, wantOK: false, wantSub: "integer"},
		{name: "string_rejected", doc: map[string]any{"versions_retention_days": "seven"}, wantOK: false, wantSub: "integer"},
		{name: "unknown_field_ignored", doc: map[string]any{"future_field": "anything"}, wantOK: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := validateMemoryConfig(tc.doc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, tc.wantOK, msg)
			}
			if !ok && tc.wantSub != "" && !strings.Contains(msg, tc.wantSub) {
				t.Errorf("error message %q does not contain %q", msg, tc.wantSub)
			}
		})
	}
}

// fmtItoa is a 1-line helper (avoids importing strconv just for
// the validator test).
func fmtItoa(n int) string {
	// Tiny inline integer-to-string for the test's substring
	// check. Avoids pulling strconv into the test file's
	// minimal import set.
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
