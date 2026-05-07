package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// newJournalHandlerTest builds a JournalHandler against the migrated test
// DB plus an in-memory recorder so we can assert that the priority handler
// dual-writes audit emits without needing the full Writer goroutine.
func newJournalHandlerTest(t *testing.T) (*JournalHandler, string, string, *emitRecorder) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	em := &emitRecorder{}
	h := NewJournalHandler(db, newTestLogger(), em)
	return h, userID, wsID, em
}

// seedJournalRow inserts a journal_entries row directly so the read-side
// tests don't have to wait for a Writer flush. Required-only fields are
// supplied; optional ones are NULL/empty.
func seedJournalRow(t *testing.T, h *JournalHandler, id, wsID, kind, severity, summary string, ts time.Time) {
	t.Helper()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES (?, ?, ?, ?, ?, 'normal', 'agent', ?, '{}', '{}')`,
		id, wsID, ts.UTC().Format("2006-01-02T15:04:05.000Z"), kind, severity, summary)
	if err != nil {
		t.Fatalf("seed entry %s: %v", id, err)
	}
}

func TestJournalHandler_List_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestJournalHandler_List_HappyPath(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)

	now := time.Now().UTC()
	seedJournalRow(t, h, "j_a", wsID, string(journal.EntryRunStarted), "info", "alpha", now.Add(-2*time.Minute))
	seedJournalRow(t, h, "j_b", wsID, string(journal.EntryRunCompleted), "info", "beta", now.Add(-1*time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/journal", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"next_cursor"`
		Count      int              `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 || len(resp.Entries) != 2 {
		t.Fatalf("count=%d entries=%d, want 2/2", resp.Count, len(resp.Entries))
	}
	// Newest first.
	if resp.Entries[0]["id"] != "j_b" {
		t.Errorf("ordering: first id = %v, want j_b", resp.Entries[0]["id"])
	}
}

func TestJournalHandler_List_BadLimit(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	cases := []string{"0", "501", "-1", "abc"}
	for _, lim := range cases {
		t.Run("limit="+lim, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/journal?limit="+lim, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("limit=%q: status=%d, want 400", lim, rr.Code)
			}
		})
	}
}

func TestJournalHandler_List_BadSinceUntil(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	cases := []struct {
		name string
		url  string
	}{
		{"bad since", "/api/v1/journal?since=not-a-time"},
		{"bad until", "/api/v1/journal?until=2026-99-99"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.url, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status=%d, want 400", rr.Code)
			}
		})
	}
}

func TestJournalHandler_List_BadPriority(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal?priority=urgent", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "priority must be one of") {
		t.Errorf("error message should explain allowed values; got %s", rr.Body.String())
	}
}

func TestJournalHandler_List_FTSQueryTooLong(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	long := strings.Repeat("a", 201)
	req := httptest.NewRequest("GET", "/api/v1/journal?q="+long, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversized q: status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestJournalHandler_List_Filters(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	now := time.Now().UTC()
	seedJournalRow(t, h, "j_run", wsID, string(journal.EntryRunStarted), "info", "run", now.Add(-3*time.Minute))
	seedJournalRow(t, h, "j_warn", wsID, string(journal.EntryKeeperDecision), "warn", "warn", now.Add(-2*time.Minute))
	seedJournalRow(t, h, "j_err", wsID, string(journal.EntryRunFailed), "error", "err", now.Add(-1*time.Minute))

	cases := []struct {
		name string
		qs   string
		want []string
	}{
		{"by entry_type", "entry_type=run.started,run.failed", []string{"j_err", "j_run"}},
		{"by severity", "severity=warn,error", []string{"j_err", "j_warn"}},
		{"exclude entry_type", "exclude_entry_type=keeper.decision", []string{"j_err", "j_run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/journal?"+c.qs, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var resp struct {
				Entries []map[string]any `json:"entries"`
			}
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			got := make([]string, 0, len(resp.Entries))
			for _, e := range resp.Entries {
				got = append(got, e["id"].(string))
			}
			if !equalStringSet(got, c.want) {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestJournalHandler_Get_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/j_x", nil)
	req.SetPathValue("id", "j_x")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestJournalHandler_Get_HappyPath(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedJournalRow(t, h, "j_get", wsID, string(journal.EntryRunStarted), "info", "hello", time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/journal/j_get", nil)
	req.SetPathValue("id", "j_get")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry["id"] != "j_get" || entry["summary"] != "hello" {
		t.Errorf("payload: %+v", entry)
	}
}

func TestJournalHandler_Get_NotFound(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/j_does_not_exist", nil)
	req.SetPathValue("id", "j_does_not_exist")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rr.Code)
	}
}

func TestJournalHandler_Get_CrossTenantNotFound(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	// Seed a row in a sibling workspace and expect 404 (not 200) when
	// queried from `wsID`. Existence of the row must not leak across
	// tenants — same shape as plain "not found".
	wsOther := "ws-other-tenant"
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsOther); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	seedJournalRow(t, h, "j_other", wsOther, string(journal.EntryRunStarted), "info", "theirs", time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/journal/j_other", nil)
	req.SetPathValue("id", "j_other")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (no cross-tenant existence leak)", rr.Code)
	}
}

func TestJournalHandler_Count_Filters(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	now := time.Now().UTC()
	for i, kind := range []string{
		string(journal.EntryRunStarted),
		string(journal.EntryRunCompleted),
		string(journal.EntryRunStarted),
		string(journal.EntryKeeperDecision),
	} {
		seedJournalRow(t, h, "j_c"+string(rune('a'+i)), wsID, kind, "info", "x", now.Add(-time.Duration(i)*time.Minute))
	}

	cases := []struct {
		name string
		qs   string
		want int64
	}{
		{"all", "", 4},
		{"by type", "entry_type=run.started", 2},
		{"by exclude", "exclude_entry_type=keeper.decision", 3},
		{"none match", "entry_type=eval.metric", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/v1/journal/count"
			if c.qs != "" {
				path += "?" + c.qs
			}
			req := httptest.NewRequest("GET", path, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Count(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var resp struct {
				Total int64 `json:"total"`
			}
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if resp.Total != c.want {
				t.Errorf("total=%d want %d", resp.Total, c.want)
			}
		})
	}
}

func TestJournalHandler_Count_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/count", nil)
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestJournalHandler_Count_IgnoresCursorAndLimit(t *testing.T) {
	// Even with `cursor` and `limit` in the query string, the count must
	// reflect the full filtered result set.  This pins the contract that
	// pagination params don't accidentally trim the badge.
	h, userID, wsID, _ := newJournalHandlerTest(t)
	for i := 0; i < 5; i++ {
		seedJournalRow(t, h, "j_p"+string(rune('a'+i)), wsID,
			string(journal.EntryRunStarted), "info", "p", time.Time{})
	}
	req := httptest.NewRequest("GET", "/api/v1/journal/count?limit=2&cursor=2026-04-30T12:00:00.000Z|j_x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 5 {
		t.Errorf("total=%d want 5 (cursor/limit must be ignored)", resp.Total)
	}
}

// TestJournalHandler_Count_AcceptsOversizedLimit guards CodeRabbit's
// finding on PR #283: when the count handler called parseJournalQuery
// directly, oversized `limit` values triggered a `1..500` 400. The
// fix strips both pagination params before parsing — verify a value
// the list view would reject (limit=999) returns the full count here.
func TestJournalHandler_Count_AcceptsOversizedLimit(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	for i := 0; i < 3; i++ {
		seedJournalRow(t, h, "j_o"+string(rune('a'+i)), wsID,
			string(journal.EntryRunStarted), "info", "o", time.Time{})
	}
	req := httptest.NewRequest("GET", "/api/v1/journal/count?limit=999", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("count?limit=999: status=%d body=%s (want 200; pagination must be stripped)",
			rr.Code, rr.Body.String())
	}
	var resp struct {
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Errorf("total=%d want 3", resp.Total)
	}
}

// TestJournalHandler_Count_AcceptsMalformedCursor is the cursor-side
// counterpart: a malformed `cursor` would normally fail decode in
// parseJournalQuery, but is meaningless for count and must be silently
// ignored so the badge still renders.
func TestJournalHandler_Count_AcceptsMalformedCursor(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedJournalRow(t, h, "j_one", wsID, string(journal.EntryRunStarted), "info", "o", time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/journal/count?cursor=garbage-no-separator", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("count?cursor=garbage: status=%d body=%s (want 200; pagination must be stripped)",
			rr.Code, rr.Body.String())
	}
}

func TestJournalHandler_SetPriority_RequiresOwnerOrAdmin(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedJournalRow(t, h, "j_p", wsID, string(journal.EntryRunStarted), "info", "p", time.Time{})

	body, _ := json.Marshal(map[string]string{"priority": "high"})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_p/priority", bytes.NewReader(body))
	req.SetPathValue("id", "j_p")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER role: status=%d want 403", rr.Code)
	}
}

func TestJournalHandler_SetPriority_RejectsBadJSON(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("POST", "/api/v1/journal/j_x/priority",
		strings.NewReader("{not-json"))
	req.SetPathValue("id", "j_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rr.Code)
	}
}

func TestJournalHandler_SetPriority_RejectsBadEnum(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	body, _ := json.Marshal(map[string]string{"priority": "URGENT"})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_x/priority",
		bytes.NewReader(body))
	req.SetPathValue("id", "j_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rr.Code)
	}
}

func TestJournalHandler_SetPriority_NotFound(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	body, _ := json.Marshal(map[string]string{"priority": "high", "reason": "test"})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_missing/priority",
		bytes.NewReader(body))
	req.SetPathValue("id", "j_missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rr.Code)
	}
}

func TestJournalHandler_SetPriority_HappyPath_AndAuditEmit(t *testing.T) {
	h, userID, wsID, em := newJournalHandlerTest(t)
	seedJournalRow(t, h, "j_p", wsID, string(journal.EntryKeeperDecision), "warn", "denied prod ssh", time.Time{})

	body, _ := json.Marshal(map[string]string{
		"priority": "permanent",
		"reason":   "FX compliance",
	})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_p/priority", bytes.NewReader(body))
	req.SetPathValue("id", "j_p")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Row was updated.
	var got string
	if err := h.db.QueryRow(`SELECT priority FROM journal_entries WHERE id = ?`, "j_p").Scan(&got); err != nil {
		t.Fatalf("priority lookup: %v", err)
	}
	if got != "permanent" {
		t.Errorf("priority not persisted: %q", got)
	}

	// Audit emit recorded by the test recorder.  Reason field must
	// land in the payload — that's the difference between a useful
	// audit trail and an empty one.
	if len(em.entries) != 1 {
		t.Fatalf("want 1 audit emit, got %d", len(em.entries))
	}
	a := em.entries[0]
	if a.Type != "memory.priority_changed" {
		t.Errorf("audit type: %q", a.Type)
	}
	if a.Payload["reason"] != "FX compliance" {
		t.Errorf("reason missing from audit payload: %v", a.Payload)
	}
	if a.Payload["new_priority"] != "permanent" {
		t.Errorf("new_priority missing or wrong: %v", a.Payload["new_priority"])
	}
}

// TestJournalHandler_SetPriority_CrossTenant404 confirms the workspace
// scope is enforced — passing a foreign workspace's entry id from this
// session must look like "not found", not "wrong tenant".
func TestJournalHandler_SetPriority_CrossTenant404(t *testing.T) {
	h, userID, wsID, em := newJournalHandlerTest(t)

	wsOther := "ws-other-tenant"
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o')`, wsOther); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedJournalRow(t, h, "j_other", wsOther, string(journal.EntryRunStarted), "info", "x", time.Time{})

	body, _ := json.Marshal(map[string]string{"priority": "high"})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_other/priority", bytes.NewReader(body))
	req.SetPathValue("id", "j_other")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant)", rr.Code)
	}
	if len(em.entries) != 0 {
		t.Errorf("cross-tenant request leaked an audit emit: %+v", em.entries)
	}
}

// TestParseJournalQuery_AllParams exercises every documented query
// parameter through the parser. parseJournalQuery is the front-line
// validator for the API; a regression here changes behaviour silently.
func TestParseJournalQuery_AllParams(t *testing.T) {
	url := "/api/v1/journal" +
		"?crew_id=c1&agent_id=a1&mission_id=m1&trace_id=t1" +
		"&crew_ids=c2,c3&agent_ids=a2,a3" +
		"&entry_type=run.started,run.failed" +
		"&exclude_entry_type=container.metrics" +
		"&severity=warn,error" +
		"&actor_type=agent,sidecar" +
		"&priority=high,permanent" +
		"&since=2026-01-01T00:00:00Z&until=2026-12-31T23:59:59Z" +
		"&limit=200&cursor=cur" +
		"&q=hello"
	req := httptest.NewRequest("GET", url, nil)
	q, err := parseJournalQuery(req, "ws-x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.WorkspaceID != "ws-x" || q.CrewID != "c1" || q.AgentID != "a1" ||
		q.MissionID != "m1" || q.TraceID != "t1" {
		t.Errorf("scope fields wrong: %+v", q)
	}
	if !equalStringSet(q.CrewIDs, []string{"c2", "c3"}) {
		t.Errorf("crew_ids: %v", q.CrewIDs)
	}
	if !equalStringSet(q.AgentIDs, []string{"a2", "a3"}) {
		t.Errorf("agent_ids: %v", q.AgentIDs)
	}
	if len(q.Types) != 2 || q.Types[0] != journal.EntryRunStarted ||
		q.Types[1] != journal.EntryRunFailed {
		t.Errorf("types: %v", q.Types)
	}
	if len(q.ExcludeTypes) != 1 || q.ExcludeTypes[0] != journal.EntryContainerMetrics {
		t.Errorf("exclude types: %v", q.ExcludeTypes)
	}
	if len(q.Severities) != 2 {
		t.Errorf("severities: %v", q.Severities)
	}
	if len(q.ActorTypes) != 2 {
		t.Errorf("actor types: %v", q.ActorTypes)
	}
	if len(q.Priorities) != 2 ||
		q.Priorities[0] != journal.PriorityHigh ||
		q.Priorities[1] != journal.PriorityPermanent {
		t.Errorf("priorities: %v", q.Priorities)
	}
	if q.Limit != 200 || q.Cursor != "cur" || q.FTSQuery != "hello" {
		t.Errorf("pagination/q: limit=%d cursor=%q fts=%q", q.Limit, q.Cursor, q.FTSQuery)
	}
	if q.Since.IsZero() || q.Until.IsZero() {
		t.Errorf("time bounds: since=%v until=%v", q.Since, q.Until)
	}
}

// TestParseJournalQuery_TrimsWhitespace verifies CSV inputs with
// surrounding whitespace are trimmed (so `?entry_type=a, b` parses to
// two clean values, not "a" and " b").
func TestParseJournalQuery_TrimsWhitespace(t *testing.T) {
	req := httptest.NewRequest("GET",
		"/api/v1/journal?entry_type=run.started%2C%20run.failed", nil)
	q, err := parseJournalQuery(req, "ws-x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(q.Types) != 2 ||
		q.Types[0] != journal.EntryRunStarted ||
		q.Types[1] != journal.EntryRunFailed {
		t.Errorf("trim CSV: %v", q.Types)
	}
}

// TestJournalHandler_Stream_LastEventIDResume covers the SSE resume
// path: when the client reconnects with `Last-Event-ID: <previous>`,
// the seed must skip everything older than (and including) that
// entry so the client doesn't replay history it already processed.
//
// We cancel the request context after the seed lands but before the
// poll ticker fires so the test exits deterministically without
// waiting on the 1-second tick.
func TestJournalHandler_Stream_LastEventIDResume(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)

	// Seed three entries with strictly-increasing timestamps so the
	// resume id has a well-defined "older" and "newer" set around it.
	now := time.Now().UTC()
	seedJournalRow(t, h, "j_s1", wsID, string(journal.EntryRunStarted), "info", "first", now.Add(-3*time.Minute))
	seedJournalRow(t, h, "j_s2", wsID, string(journal.EntryRunStarted), "info", "second", now.Add(-2*time.Minute))
	seedJournalRow(t, h, "j_s3", wsID, string(journal.EntryRunStarted), "info", "third", now.Add(-1*time.Minute))

	cases := []struct {
		name     string
		header   string
		wantSeed []string // ids that should appear in the seed batch
	}{
		{"no resume header — full seed", "", []string{"j_s1", "j_s2", "j_s3"}},
		{"resume from middle", "j_s2", []string{"j_s3"}},
		{"resume from latest — empty seed", "j_s3", []string{}},
		{"unknown id — falls back to full seed",
			"j_does_not_exist", []string{"j_s1", "j_s2", "j_s3"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/journal/stream", nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			if c.header != "" {
				req.Header.Set("Last-Event-ID", c.header)
			}
			rr := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				h.Stream(rr, req)
			}()

			// Give the seed a short window to land. The poll ticker
			// runs every second, so 100ms is far inside the seed
			// phase.  Then cancel so Stream returns.
			time.Sleep(100 * time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Stream did not return after ctx cancel")
			}

			// Parse the SSE body for `id:` lines so we can compare
			// the seed batch ids without depending on event ordering
			// across the data field.
			body := rr.Body.String()
			gotIDs := []string{}
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "id: ") {
					gotIDs = append(gotIDs, strings.TrimPrefix(line, "id: "))
				}
			}
			if !equalStringSet(gotIDs, c.wantSeed) {
				t.Errorf("seed ids: got %v, want %v\n--- body ---\n%s", gotIDs, c.wantSeed, body)
			}
		})
	}
}

// equalStringSet checks two slices contain the same elements ignoring
// order. Several handler tests use it to assert filtered-list contents
// without locking in a specific row order.
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
