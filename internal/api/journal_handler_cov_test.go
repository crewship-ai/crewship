package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// covJrnFullRow inserts a journal_entries row with every optional column
// populated (crew_id, agent_id, mission_id, actor_id, trace_id, payload,
// refs). The existing seedJournalRow helper only fills the required
// columns, leaving the optional-field branches of serializeEntries
// uncovered — this helper exercises them. Prefixed covJrn per the task's
// new-helper naming rule so it can't collide with shared helpers.
func covJrnFullRow(t *testing.T, h *JournalHandler, id, wsID string, ts time.Time) {
	t.Helper()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	// crew_id / agent_id / mission_id carry FK constraints, so seed real
	// parent rows keyed off the entry id (unique per call).
	crewID := seedCrewRow(t, h.db, "crew-"+id, wsID, "Crew "+id, "crew-"+id)
	agentID := seedAgentRow(t, h.db, "agent-"+id, wsID, crewID, "Agent "+id, "agent-"+id, "AGENT")
	missionID := seedMissionRow(t, h.db, "mission-"+id, wsID, crewID, "Mission "+id)
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries
		  (id, workspace_id, crew_id, agent_id, mission_id, ts, entry_type,
		   severity, priority, actor_type, actor_id, summary, payload, refs,
		   trace_id, span_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'normal', 'agent', ?, ?, ?, ?, ?, ?)`,
		id, wsID, crewID, agentID, missionID,
		ts.UTC().Format("2006-01-02T15:04:05.000Z"),
		string(journal.EntryRunStarted), "info", "actor-1", "full row",
		`{"k":"v"}`, `{"parent_entry_id":"p1"}`, "trace-"+id, "span-1")
	if err != nil {
		t.Fatalf("covJrn seed full row %s: %v", id, err)
	}
}

// covJrnSSEIDs scrapes the `id:` lines out of an SSE response body so a
// test can assert which entries were framed and emitted.
func covJrnSSEIDs(body string) []string {
	ids := []string{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id: ") {
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		}
	}
	return ids
}

// TestCovJrnNewHandlerNilEmitter pins the constructor's nil-emitter
// fallback: passing nil must substitute noopEmitter rather than store a
// nil interface that panics on first Emit. We then drive a priority
// change through the handler to prove the noop emitter is callable.
func TestCovJrnNewHandlerNilEmitter(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), nil)
	if h.journal == nil {
		t.Fatal("nil emitter should fall back to noopEmitter, got nil")
	}
	seedJournalRow(t, h, "j_noop", wsID, string(journal.EntryRunStarted), "info", "x", time.Time{})

	body, _ := json.Marshal(map[string]string{"priority": "high", "reason": "r"})
	req := httptest.NewRequest("POST", "/api/v1/journal/j_noop/priority", strings.NewReader(string(body)))
	req.SetPathValue("id", "j_noop")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("noop-emitter priority change: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovJrnGetEmptyID covers the 400 branch in Get when the path value
// is blank (the route matched but {id} resolved empty).
func TestCovJrnGetEmptyID(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/", nil)
	req.SetPathValue("id", "")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty id: status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovJrnSetPriorityEmptyID covers the 400 branch in SetPriority when
// the path value is blank. OWNER role so we get past the role gate and
// reach the id check.
func TestCovJrnSetPriorityEmptyID(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	body, _ := json.Marshal(map[string]string{"priority": "high"})
	req := httptest.NewRequest("POST", "/api/v1/journal//priority", strings.NewReader(string(body)))
	req.SetPathValue("id", "")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty id: status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovJrnSetPriorityRequiresWorkspace covers the 401 branch in
// SetPriority (no workspace in context).
func TestCovJrnSetPriorityRequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("POST", "/api/v1/journal/j_x/priority", strings.NewReader("{}"))
	req.SetPathValue("id", "j_x")
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

// TestCovJrnSerializeAllOptionalFields drives a fully-populated entry
// through List so every optional-field branch of serializeEntries fires
// (crew_id, agent_id, mission_id, actor_id, trace_id, payload, refs).
func TestCovJrnSerializeAllOptionalFields(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	covJrnFullRow(t, h, "j_full", wsID, time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/journal", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(resp.Entries))
	}
	e := resp.Entries[0]
	for _, k := range []string{"crew_id", "agent_id", "mission_id", "actor_id", "trace_id", "payload", "refs"} {
		if _, ok := e[k]; !ok {
			t.Errorf("optional field %q missing from serialized entry: %+v", k, e)
		}
	}
	if e["crew_id"] != "crew-j_full" || e["trace_id"] != "trace-j_full" {
		t.Errorf("optional field values wrong: %+v", e)
	}
}

// TestCovJrnStreamRequiresWorkspace covers the 401 branch in Stream.
func TestCovJrnStreamRequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/stream", nil)
	rr := httptest.NewRecorder()
	h.Stream(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

// TestCovJrnStreamBadQuery covers the 400 branch in Stream when a query
// param fails parsing (parseJournalQuery error before the flusher check).
func TestCovJrnStreamBadQuery(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/stream?priority=bogus", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Stream(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// covJrnNoFlushWriter is an http.ResponseWriter that deliberately does
// NOT implement http.Flusher, so Stream takes its "streaming not
// supported" 500 branch. httptest.ResponseRecorder implements Flusher,
// so we need a bespoke writer to reach this path.
type covJrnNoFlushWriter struct {
	header http.Header
	code   int
	body   strings.Builder
}

func (w *covJrnNoFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *covJrnNoFlushWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func (w *covJrnNoFlushWriter) WriteHeader(code int)        { w.code = code }

// TestCovJrnStreamNoFlusher covers the 500 branch when the
// ResponseWriter is not an http.Flusher.
func TestCovJrnStreamNoFlusher(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/stream", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	w := &covJrnNoFlushWriter{}
	h.Stream(w, req)
	if w.code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 (non-flusher writer)", w.code)
	}
}

// TestCovJrnStreamFreshEmptyThenLivePoll covers two otherwise-uncovered
// Stream branches at once:
//   - the "brand-new client, empty seed" path that starts the live tail
//     from time.Now() (no Last-Event-ID, no rows at connect time), and
//   - the 1s poll loop emitting a row inserted after connect.
//
// A row written after the stream attaches must arrive via the poll tick;
// we wait just past one tick then cancel so the loop's ctx.Done() branch
// also fires. Uses a fully-populated row so the poll-path serializer
// covers the optional fields too.
func TestCovJrnStreamFreshEmptyThenLivePoll(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/journal/stream", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Stream(rr, req)
	}()

	// Let the empty seed land and the live tail watermark settle to "now".
	time.Sleep(150 * time.Millisecond)
	// Insert a row strictly newer than the watermark so the poll tick
	// picks it up.
	covJrnFullRow(t, h, "j_live", wsID, time.Now().UTC().Add(2*time.Second))

	// Wait past one 1s poll tick so the entry is emitted.
	time.Sleep(1300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after ctx cancel")
	}

	got := covJrnSSEIDs(rr.Body.String())
	found := false
	for _, id := range got {
		if id == "j_live" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-poll entry j_live not emitted; got ids=%v\nbody=%s", got, rr.Body.String())
	}
}
