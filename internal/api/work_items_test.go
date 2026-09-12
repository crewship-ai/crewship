package api

// Tests for the work-ledger read surface and its two operator actions.
//
// The interesting assertions are the ones about what the API is NOT allowed to
// say: that a cancel which stopped nothing does not report `cancelled`, that a
// replay whose payload has expired explains itself instead of failing on a
// NULL, and that no response carries the accepted input — which for a chat or
// assignment source is the conversation itself.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

// seedWorkItem inserts one work item directly. It writes the ledger's columns
// rather than going through work.Store so a test can put a row into a state
// (running, terminal, expired payload) that the accept path alone cannot
// produce.
type seededWork struct {
	ID          string
	WorkspaceID string
	State       string
	Class       string
	Source      string
	SourceRef   string
	AgentID     string
	SessionID   string
	InputJSON   string
	Generation  int64
	Attempts    int
	TerminalAt  string
}

// seedWorkAttempt gives a work item the open attempt its live state implies.
// Seeding a running item with no attempt described a state the ledger cannot
// be in, and the store now refuses to guess which run a cancel is meant for.
func seedWorkAttempt(t *testing.T, db *sql.DB, workID, runID string, generation int64) {
	t.Helper()
	now := tsformat.Format(time.Now().UTC())
	if _, err := db.Exec(`
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner,
			lease_expires_at, heartbeat_at, started_at, runtime_phase)
		VALUES (?, ?, ?, ?, 'test', ?, ?, ?, 'confirmed')`,
		runID, workID, generation, generation, now, now, now); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}

func seedWorkItem(t *testing.T, db *sql.DB, w seededWork) string {
	t.Helper()
	if w.Class == "" {
		w.Class = "background"
	}
	if w.Source == "" {
		w.Source = "manual"
	}
	if w.InputJSON == "" {
		w.InputJSON = `{}`
	}
	now := tsformat.Format(time.Now().UTC())
	var terminal any
	if w.TerminalAt != "" {
		terminal = w.TerminalAt
	} else if work.State(w.State).Terminal() {
		terminal = now
	}
	_, err := db.Exec(`
		INSERT INTO work_items (id, workspace_id, source, source_ref, domain_kind, domain_id,
			agent_id, crew_id, session_id, class, authorized_by_user_id, authorized_scope,
			input_json, input_sha256, target_revision, state, state_reason, generation, attempts,
			priority, eligible_at, created_at, updated_at, terminal_at)
		VALUES (?,?,?,?,'','',?,'',?,?,'seed-author','OWNER',?,'sha-seed','rev-1',?,'',?,?,0,?,?,?,?)`,
		w.ID, w.WorkspaceID, w.Source, w.SourceRef, w.AgentID, w.SessionID, w.Class,
		w.InputJSON, w.State, w.Generation, w.Attempts, now, now, now, terminal)
	if err != nil {
		t.Fatalf("seed work item %s: %v", w.ID, err)
	}
	if _, err := db.Exec(`
		INSERT INTO work_events (work_id, seq, at, from_state, to_state, run_id, generation, reason, detail_json)
		VALUES (?, 1, ?, '', 'queued', '', 0, 'accepted', ?)`,
		w.ID, now, `{"secret_detail":"never-publish-this"}`); err != nil {
		t.Fatalf("seed work event %s: %v", w.ID, err)
	}
	return w.ID
}

func workReq(t *testing.T, method, target, body, userID, wsID, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
}

// TestWorkItemsList_FiltersFenceAndPaginate covers the four filters, the
// workspace fence and the keyset page in one table, because they are one
// query and a regression in any of them is a regression in all of them.
func TestWorkItemsList_FiltersFenceAndPaginate(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('work-foreign','Foreign','work-foreign')`); err != nil {
		t.Fatal(err)
	}
	h := NewWorkItemsHandler(db, quietLogger())

	seedWorkItem(t, db, seededWork{ID: "wk-a", WorkspaceID: ws, State: "queued", Class: "chat", Source: "chat", AgentID: "agent-1"})
	seedWorkItem(t, db, seededWork{ID: "wk-b", WorkspaceID: ws, State: "running", Class: "background", Source: "webhook", AgentID: "agent-2"})
	seedWorkItem(t, db, seededWork{ID: "wk-c", WorkspaceID: ws, State: "succeeded", Class: "background", Source: "schedule", AgentID: "agent-1"})
	seedWorkItem(t, db, seededWork{ID: "wk-foreign", WorkspaceID: "work-foreign", State: "queued"})

	cases := []struct {
		name  string
		query string
		code  int
		want  []string
	}{
		{"no filter lists the workspace only", "", 200, []string{"wk-a", "wk-b", "wk-c"}},
		{"state", "?state=running", 200, []string{"wk-b"}},
		{"class", "?class=chat", 200, []string{"wk-a"}},
		{"source", "?source=schedule", 200, []string{"wk-c"}},
		{"agent", "?agent_id=agent-1", 200, []string{"wk-a", "wk-c"}},
		{"combined", "?agent_id=agent-1&state=queued", 200, []string{"wk-a"}},
		{"cursor", "?after=wk-a", 200, []string{"wk-b", "wk-c"}},
		{"a misspelt state is refused, not answered empty", "?state=runnign", 400, nil},
		{"a misspelt class is refused", "?class=chatt", 400, nil},
		{"a misspelt source is refused", "?source=webhooks", 400, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.List(rr, workReq(t, "GET", "/api/v1/workspaces/"+ws+"/work-items"+tc.query, "", user, ws, "MEMBER"))
			if rr.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.code, rr.Body.String())
			}
			if tc.code != 200 {
				return
			}
			var page workItemPage
			decodeJSON(t, rr, &page)
			var got []string
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("items %v, want %v", got, tc.want)
			}
			if strings.Contains(rr.Body.String(), "wk-foreign") {
				t.Fatal("a foreign workspace's work leaked into the page")
			}
		})
	}
}

// The page truncates at 100 and hands back the cursor that resumes it. A list
// endpoint that returns everything is the one that takes the server down when
// a webhook storm files ten thousand items.
func TestWorkItemsList_KeysetPageCapsAt100(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWorkItemsHandler(db, quietLogger())
	for i := 0; i < 105; i++ {
		seedWorkItem(t, db, seededWork{ID: fmt.Sprintf("wk-%03d", i), WorkspaceID: ws, State: "queued"})
	}
	rr := httptest.NewRecorder()
	h.List(rr, workReq(t, "GET", "/work-items", "", user, ws, "MEMBER"))
	var page workItemPage
	decodeJSON(t, rr, &page)
	if len(page.Items) != 100 {
		t.Fatalf("page of %d, want 100", len(page.Items))
	}
	if page.NextCursor == nil || *page.NextCursor != "wk-099" {
		t.Fatalf("next_cursor %v, want wk-099", page.NextCursor)
	}
	rr = httptest.NewRecorder()
	h.List(rr, workReq(t, "GET", "/work-items?after="+*page.NextCursor, "", user, ws, "MEMBER"))
	decodeJSON(t, rr, &page)
	if len(page.Items) != 5 || page.NextCursor != nil {
		t.Fatalf("second page: %d items, cursor %v", len(page.Items), page.NextCursor)
	}
}

// TestWorkItemGet_CarriesAttemptsAndHistoryAndNoPayload is the §9 privacy
// assertion. The input is the conversation for a chat-sourced item, and
// detail_json is written by whatever produced the transition — neither may
// reach a client, and both are one careless SELECT * away from doing so.
func TestWorkItemGet_CarriesAttemptsAndHistoryAndNoPayload(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWorkItemsHandler(db, quietLogger())

	const secret = "the-user-typed-this-in-chat"
	seedWorkItem(t, db, seededWork{ID: "wk-detail", WorkspaceID: ws, State: "running", Source: "chat",
		SessionID: "sess-1", InputJSON: `{"message":"` + secret + `"}`, Generation: 2, Attempts: 1})
	now := tsformat.Format(time.Now().UTC())
	if _, err := db.Exec(`
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner, lease_expires_at,
			heartbeat_at, runtime_locator, started_at, start_reason, cost_usd)
		VALUES ('run-1','wk-detail',1,2,'worker-a',?,?,'container:abc',?,'claimed',0.25)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	req := workReq(t, "GET", "/work-items/wk-detail", "", user, ws, "VIEWER")
	req.SetPathValue("workItemId", "wk-detail")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	var detail workItemDetailView
	decodeJSON(t, rr, &detail)
	if detail.ID != "wk-detail" || detail.State != "running" || detail.SessionID != "sess-1" {
		t.Fatalf("detail: %+v", detail)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].RunID != "run-1" || detail.Attempts[0].CostUSD != 0.25 {
		t.Fatalf("attempts: %+v", detail.Attempts)
	}
	if len(detail.Events) != 1 || detail.Events[0].ToState != "queued" {
		t.Fatalf("events: %+v", detail.Events)
	}
	for _, forbidden := range []string{secret, "input_json", "detail_json", "never-publish-this"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("the detail response leaks %q: %s", forbidden, rr.Body.String())
		}
	}
	if detail.InputSHA256 == "" {
		t.Fatal("input_sha256 is what replaces the payload; it must be present")
	}
}

// A work item in another workspace is absent, not forbidden: a 403 would
// confirm the id exists to a caller with no business knowing that.
func TestWorkItemGet_ForeignWorkspaceIs404(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('wk-other','Other','wk-other')`); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-hidden", WorkspaceID: "wk-other", State: "running"})
	h := NewWorkItemsHandler(db, quietLogger())

	for _, id := range []string{"wk-hidden", "wk-does-not-exist", ""} {
		req := workReq(t, "GET", "/work-items/"+id, "", user, ws, "OWNER")
		req.SetPathValue("workItemId", id)
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != 404 {
			t.Fatalf("id %q: status %d, want 404: %s", id, rr.Code, rr.Body.String())
		}
	}
}

// TestWorkCancel_SaysWhatItActuallyDid is the §4 assertion the whole endpoint
// exists for: a cancel is a REQUEST, and only a confirmed stop is `cancelled`.
func TestWorkCancel_SaysWhatItActuallyDid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		state       string
		wantOutcome string
		wantState   string
	}{
		{"queued work stops atomically", "queued", workCancelOutcomeCancelled, "cancelled"},
		{"work waiting to retry stops atomically", "retry_wait", workCancelOutcomeCancelled, "cancelled"},
		{"a live runtime is signalled, not declared dead", "running", workCancelOutcomeRequested, "running"},
		{"a claimed item is signalled too", "starting", workCancelOutcomeRequested, "starting"},
		{"a parked item is signalled", "waiting", workCancelOutcomeRequested, "waiting"},
		{"unclear ownership is signalled, and keeps its capacity", "needs_reconciliation", workCancelOutcomeRequested, "needs_reconciliation"},
		{"a completion that won the race reports the real state", "succeeded", workCancelOutcomeTerminal, "succeeded"},
		{"so does a failure", "failed", workCancelOutcomeTerminal, "failed"},
		{"a repeat of a completed cancel is idempotent", "cancelled", workCancelOutcomeCancelled, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			h := NewWorkItemsHandler(db, quietLogger())
			seedWorkItem(t, db, seededWork{ID: "wk-cancel", WorkspaceID: ws, State: tc.state, Generation: 3})
			// A state that holds a runtime has an open attempt behind it. The
			// store now refuses to guess which run a cancel is meant for, so
			// seeding the item alone would describe a row the ledger cannot
			// hold — and the honest answer to it is reconciliation, not a
			// cancel. needs_reconciliation is deliberately left without one:
			// that state IS "the attempt is over and nobody knows what it did".
			if work.State(tc.state).Live() || work.State(tc.state) == work.StateWaiting {
				seedWorkAttempt(t, db, "wk-cancel", "run-cancel", 3)
			}

			req := workReq(t, "POST", "/work-items/wk-cancel/cancel", "", user, ws, "MANAGER")
			req.SetPathValue("workItemId", "wk-cancel")
			rr := httptest.NewRecorder()
			h.Cancel(rr, req)
			if rr.Code != 200 {
				t.Fatalf("cancel: %d %s", rr.Code, rr.Body.String())
			}
			var out workCancelResponse
			decodeJSON(t, rr, &out)
			if out.Outcome != tc.wantOutcome || out.State != tc.wantState {
				t.Fatalf("outcome %q state %q, want %q / %q (%s)", out.Outcome, out.State, tc.wantOutcome, tc.wantState, out.Detail)
			}
			if out.Detail == "" {
				t.Fatal("the response must explain what happened; a bare outcome is not actionable")
			}

			// Whatever it claimed, the ledger must agree.
			var stored string
			if err := db.QueryRow(`SELECT state FROM work_items WHERE id='wk-cancel'`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != tc.wantState {
				t.Fatalf("ledger says %q, response said %q", stored, tc.wantState)
			}
		})
	}
}

// A cancel request against a live runtime is recorded once per attempt, no
// matter how many times a UI polls it. Without the generation key, a client
// that retries on a slow response grows the history without bound.
func TestWorkCancel_RequestIsRecordedOnceAndIsIdempotent(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWorkItemsHandler(db, quietLogger())
	seedWorkItem(t, db, seededWork{ID: "wk-signal", WorkspaceID: ws, State: "running", Generation: 4})
	// Running work has an open attempt. Seeding the item alone described a
	// state the ledger cannot actually be in, and the store now says so rather
	// than guessing which run a cancel is meant for.
	seedWorkAttempt(t, db, "wk-signal", "run-signal", 4)

	for i := 0; i < 3; i++ {
		req := workReq(t, "POST", "/work-items/wk-signal/cancel", "", user, ws, "MANAGER")
		req.SetPathValue("workItemId", "wk-signal")
		rr := httptest.NewRecorder()
		h.Cancel(rr, req)
		if rr.Code != 200 {
			t.Fatalf("cancel %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	var requests int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM work_events WHERE work_id='wk-signal' AND reason LIKE ?`,
		cancelRequestedReason+"%").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("%d cancel-request events after three calls, want 1", requests)
	}
	// The request is durable — it survives the process that answered the call.
	var reason, from, to string
	if err := db.QueryRow(
		`SELECT reason, from_state, to_state FROM work_events WHERE work_id='wk-signal' AND reason LIKE ?`,
		cancelRequestedReason+"%").Scan(&reason, &from, &to); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, user) {
		t.Fatalf("the request must name who asked, got %q", reason)
	}
	if from != "running" || to != "running" {
		t.Fatalf("a request is not a transition; got %s -> %s", from, to)
	}
	// A NEW attempt (a bumped generation) may be cancelled again.
	if _, err := db.Exec(`UPDATE work_items SET generation=5 WHERE id='wk-signal'`); err != nil {
		t.Fatal(err)
	}
	req := workReq(t, "POST", "/work-items/wk-signal/cancel", "", user, ws, "MANAGER")
	req.SetPathValue("workItemId", "wk-signal")
	h.Cancel(httptest.NewRecorder(), req)
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM work_events WHERE work_id='wk-signal' AND reason LIKE ?`,
		cancelRequestedReason+"%").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("%d cancel-request events after a generation bump, want 2", requests)
	}
}

func TestWorkCancel_ForeignWorkspaceIs404(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('wk-far','Far','wk-far')`); err != nil {
		t.Fatal(err)
	}
	seedWorkItem(t, db, seededWork{ID: "wk-far-item", WorkspaceID: "wk-far", State: "running"})
	h := NewWorkItemsHandler(db, quietLogger())
	req := workReq(t, "POST", "/work-items/wk-far-item/cancel", "", user, ws, "OWNER")
	req.SetPathValue("workItemId", "wk-far-item")
	rr := httptest.NewRecorder()
	h.Cancel(rr, req)
	if rr.Code != 404 {
		t.Fatalf("status %d, want 404", rr.Code)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM work_items WHERE id='wk-far-item'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("a cross-workspace cancel changed the other workspace's item to %q", state)
	}
}

// TestWorkReplay_MintsNewWorkCarryingTheCallersAuthorization pins §4: a replay
// is a new authorization, not a continuation.
func TestWorkReplay_MintsNewWorkCarryingTheCallersAuthorization(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWorkItemsHandler(db, quietLogger())
	const payload = `{"message":"replay-me-verbatim"}`
	seedWorkItem(t, db, seededWork{ID: "wk-done", WorkspaceID: ws, State: "failed", Source: "assignment",
		SessionID: "sess-original", AgentID: "agent-9", InputJSON: payload})

	req := workReq(t, "POST", "/work-items/wk-done/replay", `{"reason":"transient provider outage"}`, user, ws, "MANAGER")
	req.SetPathValue("workItemId", "wk-done")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)
	if rr.Code != 201 {
		t.Fatalf("replay: %d %s", rr.Code, rr.Body.String())
	}
	var created workItemView
	decodeJSON(t, rr, &created)
	if created.ID == "wk-done" {
		t.Fatal("a replay must mint a NEW work id, not reuse the original")
	}
	if created.ReplayOf == nil || *created.ReplayOf != "wk-done" {
		t.Fatalf("replay_of %v, want wk-done", created.ReplayOf)
	}
	if created.ReplayReason != "transient provider outage" {
		t.Fatalf("replay_reason %q", created.ReplayReason)
	}
	if created.State != "queued" {
		t.Fatalf("state %q, want queued", created.State)
	}
	if created.AgentID != "agent-9" {
		t.Fatalf("agent %q, want the original's", created.AgentID)
	}
	if created.SessionID != "" {
		t.Fatalf("session %q — a replay is a new turn and must not be dropped into the original conversation", created.SessionID)
	}
	if strings.Contains(rr.Body.String(), "replay-me-verbatim") {
		t.Fatalf("the replay response echoed the input back: %s", rr.Body.String())
	}

	var authorizedBy, storedInput, replayOf string
	if err := db.QueryRow(
		`SELECT authorized_by_user_id, input_json, COALESCE(replay_of,'') FROM work_items WHERE id=?`,
		created.ID).Scan(&authorizedBy, &storedInput, &replayOf); err != nil {
		t.Fatal(err)
	}
	if authorizedBy != user {
		t.Fatalf("authorized_by %q, want the CURRENT caller %q — a replay outliving the permission that admitted the original is the hole §4 closes", authorizedBy, user)
	}
	if storedInput != payload {
		t.Fatalf("the replay lost the original input: %q", storedInput)
	}
	if replayOf != "wk-done" {
		t.Fatalf("ledger replay_of %q", replayOf)
	}
	// The original is untouched: terminal history is never rewritten.
	var originalState string
	if err := db.QueryRow(`SELECT state FROM work_items WHERE id='wk-done'`).Scan(&originalState); err != nil {
		t.Fatal(err)
	}
	if originalState != "failed" {
		t.Fatalf("the original moved to %q; terminal history must not be rewritten", originalState)
	}
}

func TestWorkReplay_RefusesWhatItCannotReproduce(t *testing.T) {
	t.Parallel()
	past := tsformat.Format(time.Now().UTC().Add(-24 * time.Hour))
	cases := []struct {
		name     string
		state    string
		source   string
		delivery func(t *testing.T, db *sql.DB, ws string) string
		wantCode int
		wantSays string
	}{
		{
			name:     "live work is not replayable",
			state:    "running",
			source:   "manual",
			wantCode: 409,
			wantSays: "still running",
		},
		{
			name:     "queued work is not replayable either",
			state:    "queued",
			source:   "manual",
			wantCode: 409,
			wantSays: "only available once the original has finished",
		},
		{
			name:   "a dropped payload explains the retention instead of failing opaquely",
			state:  "failed",
			source: "webhook",
			delivery: func(t *testing.T, db *sql.DB, ws string) string {
				t.Helper()
				seedDelivery(t, db, seededDelivery{ID: "dlv-expired", WorkspaceID: ws, EndpointID: "ep-1",
					SourceDeliveryID: "src-expired", RawBody: nil, RawBodyExpiresAt: past})
				return "dlv-expired"
			},
			wantCode: 409,
			wantSays: "retention",
		},
		{
			name:   "a delivery pruned entirely says so as well",
			state:  "failed",
			source: "webhook",
			delivery: func(t *testing.T, db *sql.DB, ws string) string {
				t.Helper()
				return "dlv-gone"
			},
			wantCode: 409,
			wantSays: "no longer in the ledger",
		},
		{
			name:   "a payload still held replays",
			state:  "failed",
			source: "webhook",
			delivery: func(t *testing.T, db *sql.DB, ws string) string {
				t.Helper()
				seedDelivery(t, db, seededDelivery{ID: "dlv-live", WorkspaceID: ws, EndpointID: "ep-1",
					SourceDeliveryID: "src-live", RawBody: []byte(`{"ok":true}`)})
				return "dlv-live"
			},
			wantCode: 201,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			h := NewWorkItemsHandler(db, quietLogger())
			sourceRef := ""
			if tc.delivery != nil {
				sourceRef = tc.delivery(t, db, ws)
			}
			seedWorkItem(t, db, seededWork{ID: "wk-replay", WorkspaceID: ws, State: tc.state,
				Source: tc.source, SourceRef: sourceRef})

			req := workReq(t, "POST", "/work-items/wk-replay/replay", "", user, ws, "MANAGER")
			req.SetPathValue("workItemId", "wk-replay")
			rr := httptest.NewRecorder()
			h.Replay(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantSays != "" && !strings.Contains(rr.Body.String(), tc.wantSays) {
				t.Fatalf("the refusal must explain itself; %q is not in %s", tc.wantSays, rr.Body.String())
			}
		})
	}
}

// A different target revision is an explicit instruction (§4), and an unknown
// field is refused rather than silently ignored — a typo'd target_revision
// that runs against the original revision is the worst of both.
func TestWorkReplay_TargetRevisionIsExplicit(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWorkItemsHandler(db, quietLogger())

	replay := func(id, body string) *httptest.ResponseRecorder {
		seedWorkItem(t, db, seededWork{ID: id, WorkspaceID: ws, State: "succeeded"})
		req := workReq(t, "POST", "/work-items/"+id+"/replay", body, user, ws, "MANAGER")
		req.SetPathValue("workItemId", id)
		rr := httptest.NewRecorder()
		h.Replay(rr, req)
		return rr
	}

	rr := replay("wk-rev-default", "")
	if rr.Code != 201 {
		t.Fatalf("bare replay: %d %s", rr.Code, rr.Body.String())
	}
	var created workItemView
	decodeJSON(t, rr, &created)
	if created.TargetRevision != "rev-1" {
		t.Fatalf("target_revision %q, want the original's rev-1", created.TargetRevision)
	}

	rr = replay("wk-rev-explicit", `{"target_revision":"rev-9"}`)
	decodeJSON(t, rr, &created)
	if created.TargetRevision != "rev-9" {
		t.Fatalf("target_revision %q, want the explicit rev-9", created.TargetRevision)
	}

	if rr := replay("wk-rev-typo", `{"targt_revision":"rev-9"}`); rr.Code != 400 {
		t.Fatalf("an unknown field must be refused, got %d %s", rr.Code, rr.Body.String())
	}
}

// Every route in this file needs a workspace in context. A handler that ran
// without one would query with an empty workspace_id and return nothing —
// which looks like "no work" rather than like the wiring bug it is.
func TestWorkRoutes_RequireWorkspaceAndRole(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	h := NewWorkItemsHandler(db, quietLogger())
	d := NewWebhookDeliveriesHandler(db, quietLogger())
	handlers := map[string]http.HandlerFunc{
		"work list": h.List, "work get": h.Get, "cancel": h.Cancel, "replay": h.Replay,
		"delivery list": d.List, "delivery get": d.Get,
	}
	for name, fn := range handlers {
		t.Run(name+" without workspace", func(t *testing.T) {
			rr := httptest.NewRecorder()
			fn(rr, workReq(t, "POST", "/", "", user, "", "OWNER"))
			if rr.Code != 400 {
				t.Fatalf("status %d, want 400", rr.Code)
			}
		})
		t.Run(name+" without a role", func(t *testing.T) {
			rr := httptest.NewRecorder()
			fn(rr, workReq(t, "POST", "/", "", user, "some-ws", ""))
			if rr.Code != 403 {
				t.Fatalf("status %d, want 403", rr.Code)
			}
		})
	}
}

// The work store is the ledger's owner; the API must read what it writes.
// Accepting through work.Store and then reading through the handler is the
// only assertion that covers the seam between them.
func TestWorkItems_ReadWhatTheLedgerActuallyWrote(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := work.NewStore(db).AcceptTx(context.Background(), tx, work.AcceptRequest{
		WorkspaceID: ws, Source: work.SourceManual, Class: work.ClassBackground,
		AgentID: "agent-live", InputJSON: `{"do":"a thing"}`, InputSHA256: "sha-live",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	h := NewWorkItemsHandler(db, quietLogger())
	req := workReq(t, "GET", "/work-items/"+receipt.WorkID, "", user, ws, "MEMBER")
	req.SetPathValue("workItemId", receipt.WorkID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	var detail workItemDetailView
	decodeJSON(t, rr, &detail)
	if detail.State != string(work.StateQueued) || detail.InputSHA256 != "sha-live" {
		t.Fatalf("detail: %+v", detail)
	}
	if len(detail.Events) != 1 || detail.Events[0].Reason != "accepted" {
		t.Fatalf("the accept event must be visible: %+v", detail.Events)
	}
	if detail.Events[0].At == "" {
		t.Fatal("an event with no timestamp cannot be ordered by a client")
	}
}

func TestWorkResolve_RequiresEvidenceAndCurrentGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, role, state, body string
		status                  int
	}{
		{"resolve", "MANAGER", "needs_reconciliation", `{"state":"failed","generation":3,"runtime_stopped":true,"reason":"checked container and provider receipt"}`, 200},
		{"stale generation", "MANAGER", "needs_reconciliation", `{"state":"failed","generation":2,"runtime_stopped":true,"reason":"checked"}`, 409},
		{"not reconciliating", "MANAGER", "running", `{"state":"failed","generation":3,"runtime_stopped":true,"reason":"checked"}`, 409},
		{"missing stop evidence", "MANAGER", "needs_reconciliation", `{"state":"failed","generation":3,"reason":"checked"}`, 400},
		{"missing reason", "MANAGER", "needs_reconciliation", `{"state":"failed","generation":3,"runtime_stopped":true}`, 400},
		{"implicit retry", "MANAGER", "needs_reconciliation", `{"state":"retry_wait","generation":3,"runtime_stopped":true,"reason":"checked"}`, 400},
		{"reader", "VIEWER", "needs_reconciliation", `{"state":"failed","generation":3,"runtime_stopped":true,"reason":"checked"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			seedWorkItem(t, db, seededWork{ID: "resolve-item", WorkspaceID: ws, State: tc.state, Generation: 3})
			h := NewWorkItemsHandler(db, quietLogger())
			req := workReq(t, "POST", "/work-items/resolve-item/resolve", tc.body, user, ws, tc.role)
			req.SetPathValue("workItemId", "resolve-item")
			rr := httptest.NewRecorder()
			h.Resolve(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			var state string
			if err := db.QueryRow("SELECT state FROM work_items WHERE id='resolve-item'").Scan(&state); err != nil {
				t.Fatal(err)
			}
			if tc.status == 200 {
				if state != "failed" {
					t.Fatalf("resolved state %s", state)
				}
				var reason string
				if err := db.QueryRow("SELECT reason FROM work_events WHERE work_id='resolve-item' ORDER BY seq DESC LIMIT 1").Scan(&reason); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(reason, user) || !strings.Contains(reason, "provider receipt") {
					t.Fatalf("resolution lost actor/evidence: %s", reason)
				}
			} else if state != tc.state {
				t.Fatalf("refusal changed work from %s to %s", tc.state, state)
			}
		})
	}
}
