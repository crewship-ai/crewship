package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// =============================================================================
// A routine step is not a conversation, and `LIMIT` is why that has to be
// settled in SQL.
//
// The runner mints one synthetic chat PER STEP so journal/audit can join, and
// `GET /agents/{id}/chats` returned every one of them in the same
// activity-ordered list a person's conversations live in. The sidebar reads a
// page of that list. So a five-step routine does not clutter the column, it
// EMPTIES it: five rows per run, ten rows per page, two runs and yesterday's
// thread is off the end of the query — before any client-side filter gets a
// chance to look at it.
//
// Hence `?kind=`, and hence these tests: one that the partition is total and
// its two implementations agree, and one that the filter runs BEFORE the limit.
// =============================================================================

func TestChatKindPredicatesMatchClassifier(t *testing.T) {
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)

	// Every combination the column can hold, including the ones no writer
	// produces today — a restored backup and a future origin value are both
	// rows this code will meet.
	modes := []string{"CHAT", "MISSION", "TASK"}
	origins := []any{nil, "UI", "CLI", "WEBHOOK", "CRON", "ROUTINE", "AGENT", "SOMETHING_NEW"}

	i := 0
	want := map[string]ChatKind{}
	for _, mode := range modes {
		for _, origin := range origins {
			i++
			id := fmt.Sprintf("ck-%d", i)
			execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status, origin)
				VALUES (?, 'ck-ag', ?, ?, 'ACTIVE', ?)`, id, wsID, mode, origin)
			o := ""
			if origin != nil {
				o = origin.(string)
			}
			want[id] = ChatKindOf(mode, o)
		}
	}

	// Each kind's SQL must select exactly the rows the Go classifier calls
	// that kind. Drift between the two is the failure mode that matters: the
	// server pages by one rule and the client labels by another, so a row
	// arrives in the Routines list wearing a Direct badge.
	seen := map[string]int{}
	for kind, pred := range chatKindPredicates {
		rows, err := db.Query(`SELECT c.id FROM chats c WHERE c.workspace_id = ? AND (`+pred+`)`, wsID)
		if err != nil {
			t.Fatalf("kind %s: %v", kind, err)
		}
		func() {
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				seen[id]++
				if want[id] != kind {
					t.Errorf("row %s: SQL says %s, ChatKindOf says %s", id, kind, want[id])
				}
			}
		}()
	}

	// Total AND disjoint. A row matched by nothing is a row that vanishes
	// from every scope — unfindable, with no error anywhere to say so — and a
	// row matched twice is one that shows up in two lists.
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("row %s matched %d predicates, want exactly 1", id, seen[id])
		}
	}
}

func TestParseChatKinds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantAll bool // no narrowing
		wantErr bool
		// wantKinds is what the fragment must SELECT, asserted by running it.
		// Substring-matching generated SQL is what this test used to do, and
		// it broke the day `c.mode <> 'MISSION'` became
		// `COALESCE(c.mode,'') <> 'MISSION'` — a change that fixed a real hole
		// (a NULL mode matched no predicate at all) and altered nothing the
		// test was actually there to protect. A brittle assertion that goes
		// red for a correct change teaches its reader to edit the test
		// without reading it.
		wantKinds []ChatKind
	}{
		// Absent and "all" are the pre-change behaviour, byte for byte. Every
		// existing caller — the CLI, anything scripted — keeps its answer.
		{name: "absent", raw: "", wantAll: true},
		{name: "explicit all", raw: "all", wantAll: true},
		{name: "whitespace only", raw: "   ", wantAll: true},
		// Every kind at once is the same query as no filter, and asking
		// SQLite to evaluate a four-branch tautology per row is not free.
		{name: "every kind collapses", raw: "direct,routine,issue,agent", wantAll: true},
		{name: "one kind", raw: "direct", wantKinds: []ChatKind{ChatKindDirect}},
		{name: "two kinds", raw: "routine,issue", wantKinds: []ChatKind{ChatKindRoutine, ChatKindIssue}},
		{name: "case and spacing", raw: " Routine , ISSUE ",
			wantKinds: []ChatKind{ChatKindRoutine, ChatKindIssue}},
		// A typo must not silently widen the list back to everything. That is
		// the failure the whole parameter exists to prevent, arriving through
		// the front door.
		{name: "unknown kind", raw: "routines", wantErr: true},
		{name: "unknown among known", raw: "direct,nonsense", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			where, err := parseChatKinds(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseChatKinds(%q) = %q, want error", tc.raw, where)
				}
				if !strings.Contains(err.Error(), "direct") {
					t.Errorf("error should name the vocabulary, got %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChatKinds(%q): %v", tc.raw, err)
			}
			if tc.wantAll && where != "" {
				t.Fatalf("parseChatKinds(%q) = %q, want no narrowing", tc.raw, where)
			}
			if len(tc.wantKinds) == 0 {
				return
			}
			// Run it. The fragment's job is to select a set of rows, so that
			// is what gets asserted — and this doubles as proof it is valid
			// SQL when concatenated after a real WHERE, which a string match
			// cannot tell you.
			db := setupTestDB(t)
			wsID := chatKindSeed(t, db)
			want := map[string]bool{}
			for i, c := range []struct{ mode, origin string }{
				{"CHAT", "UI"}, {"CHAT", ""}, {"CHAT", "ROUTINE"}, {"CHAT", "CRON"},
				{"CHAT", "WEBHOOK"}, {"CHAT", "AGENT"}, {"MISSION", ""}, {"CHAT", "NEW"},
			} {
				id := fmt.Sprintf("pk-%d", i)
				var origin any
				if c.origin != "" {
					origin = c.origin
				}
				execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status, origin)
					VALUES (?, 'ck-ag', ?, ?, 'ACTIVE', ?)`, id, wsID, c.mode, origin)
				for _, k := range tc.wantKinds {
					if ChatKindOf(c.mode, c.origin) == k {
						want[id] = true
					}
				}
			}
			rows, err := db.Query(
				`SELECT c.id FROM chats c WHERE c.workspace_id = ?`+where, wsID)
			if err != nil {
				t.Fatalf("fragment is not valid SQL: %v\n%s", err, where)
			}
			defer rows.Close()
			got := map[string]bool{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				got[id] = true
			}
			if len(got) != len(want) {
				t.Errorf("parseChatKinds(%q) selected %v, want %v", tc.raw, got, want)
			}
			for id := range want {
				if !got[id] {
					t.Errorf("parseChatKinds(%q) missed %s", tc.raw, id)
				}
			}
		})
	}
}

func TestParseChatKindsIsStable(t *testing.T) {
	// Same request, same statement text — an unstable one defeats the
	// prepared-statement cache for nothing.
	a, _ := parseChatKinds("issue,routine")
	b, _ := parseChatKinds("routine,issue")
	if a != b {
		t.Errorf("order of the parameter changed the SQL:\n %q\n %q", a, b)
	}
}

func TestListChats_KindFilterRunsBeforeTheLimit(t *testing.T) {
	// The regression this whole change is about. Two hundred routine-step
	// chats, all fresher than the one conversation a person actually had —
	// which is what a nightly five-step routine looks like after six weeks.
	//
	// Unfiltered, the conversation is not merely below the fold: the server's
	// LIMIT 100 never reaches it, so no client-side filter can ever put it
	// back. Filtered, it is the only row.
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)

	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, origin, last_activity_at)
		VALUES ('ck-human', 'ck-ag', ?, 'Deploy rollback', 'CHAT', 'ACTIVE', 'UI', '2026-08-01T09:00:00.000Z')`, wsID)
	for i := 0; i < 200; i++ {
		execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, origin, last_activity_at)
			VALUES (?, 'ck-ag', ?, ?, 'CHAT', 'ACTIVE', 'ROUTINE', ?)`,
			fmt.Sprintf("ck-run-%03d", i), wsID,
			fmt.Sprintf("Daily digest · step-%d", i),
			fmt.Sprintf("2026-08-30T10:%02d:00.000Z", i%60))
	}

	unfiltered := listChatIDs(t, db, wsID, "")
	if containsID(unfiltered, "ck-human") {
		t.Fatalf("precondition broken: the conversation should be evicted by the limit, got %d rows", len(unfiltered))
	}

	direct := listChatIDs(t, db, wsID, "direct")
	if len(direct) != 1 || direct[0] != "ck-human" {
		t.Fatalf("kind=direct returned %v, want exactly the conversation", direct)
	}

	routine := listChatIDs(t, db, wsID, "routine")
	if len(routine) != 100 {
		t.Errorf("kind=routine returned %d rows, want the full page of 100", len(routine))
	}
	if containsID(routine, "ck-human") {
		t.Error("kind=routine returned the person's conversation")
	}
}

func TestListChats_ReportsKindOnEveryRow(t *testing.T) {
	// The client must never have to re-derive the partition from mode+origin
	// — a second opinion about a page the server already cut is exactly what
	// would drift from the filter that produced it.
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status, origin)
		VALUES ('ck-a', 'ck-ag', ?, 'CHAT', 'ACTIVE', 'ROUTINE'),
		       ('ck-b', 'ck-ag', ?, 'MISSION', 'ACTIVE', NULL),
		       ('ck-c', 'ck-ag', ?, 'CHAT', 'ACTIVE', NULL)`, wsID, wsID, wsID)

	var got []chatResponse
	decodeChats(t, listChatsRaw(t, db, wsID, ""), &got)
	byID := map[string]ChatKind{}
	for _, c := range got {
		byID[c.ID] = c.Kind
	}
	for id, want := range map[string]ChatKind{
		"ck-a": ChatKindRoutine,
		"ck-b": ChatKindIssue,
		"ck-c": ChatKindDirect,
	} {
		if byID[id] != want {
			t.Errorf("%s kind = %q, want %q", id, byID[id], want)
		}
	}
}

func TestListChats_RejectsAnUnknownKind(t *testing.T) {
	// Answering a typo with the unfiltered list would hand back the exact
	// mixed column the parameter exists to avoid, while looking like success.
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)
	rr := listChatsRaw(t, db, wsID, "routines")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
}

/* ------------------------------------------------------------------ helpers */

func chatKindSeed(t *testing.T, db *sql.DB) string {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('ck-crew', ?, 'C', 'ck-c')`, wsID)
	seedAgentRow(t, db, "ck-ag", wsID, "ck-crew", "A", "ck-a", "AGENT")
	return wsID
}

func listChatsRaw(t *testing.T, db *sql.DB, wsID, kind string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewAgentHandler(db, newTestLogger())
	url := "/api/v1/agents/ck-ag/chats"
	if kind != "" {
		url += "?kind=" + kind
	}
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("agentId", "ck-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)
	return rr
}

func decodeChats(t *testing.T, rr *httptest.ResponseRecorder, into *[]chatResponse) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func listChatIDs(t *testing.T, db *sql.DB, wsID, kind string) []string {
	t.Helper()
	var rows []chatResponse
	decodeChats(t, listChatsRaw(t, db, wsID, kind), &rows)
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestInternalCreateChat_StampsOrigin — the write side of the partition.
//
// `POST /api/v1/internal/chats` is the endpoint the pipeline runner mints its
// per-step chats through, and it did not accept an origin at all. That single
// omission is why every routine step ever run arrived in the conversations
// column indistinguishable from a thread somebody opened by hand: NULL origin,
// NULL created_by, and a title as its only evidence.
func TestInternalCreateChat_StampsOrigin(t *testing.T) {
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)
	h := NewInternalHandler(db, "tok", newTestLogger())

	cases := []struct {
		name string
		sent string
		want string
	}{
		{name: "routine", sent: "ROUTINE", want: "ROUTINE"},
		{name: "delegation", sent: "AGENT", want: "AGENT"},
		// Not rejected, stored as NULL. An origin is provenance, not input the
		// caller is entitled to invent — and refusing the call would cost the
		// run its audit row, which the caller treats as non-fatal and
		// continues past. A chat with unknown provenance is still a chat.
		{name: "unknown value", sent: "NONSENSE", want: ""},
		{name: "omitted", sent: "", want: ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("ck-int-%d", i)
			body := fmt.Sprintf(`{"chat_id":%q,"agent_id":"ck-ag","workspace_id":%q,"origin":%q}`, id, wsID, tc.sent)
			req := httptest.NewRequest("POST", "/api/v1/internal/chats", strings.NewReader(body))
			rr := httptest.NewRecorder()
			h.CreateChat(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var got sql.NullString
			if err := db.QueryRow(`SELECT origin FROM chats WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got.String != tc.want {
				t.Errorf("origin = %q, want %q", got.String, tc.want)
			}
		})
	}
}

func TestListChats_KindCountsHeader(t *testing.T) {
	// The tab strip needs totals for the kinds the response deliberately does
	// NOT contain — that is the whole point of `?kind=`, and it is why the
	// counts cannot be derived from the body. Without them a bucket row can
	// only carry a number for the bucket you are already standing in, which is
	// the one difference that would make this column read unlike /routines'
	// STATUS section.
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status, origin)
		VALUES ('ck-d1','ck-ag',?,'CHAT','ACTIVE','UI'),
		       ('ck-d2','ck-ag',?,'CHAT','ACTIVE',NULL),
		       ('ck-r1','ck-ag',?,'CHAT','ACTIVE','ROUTINE'),
		       ('ck-r2','ck-ag',?,'CHAT','ACTIVE','CRON'),
		       ('ck-r3','ck-ag',?,'CHAT','ACTIVE','WEBHOOK'),
		       ('ck-i1','ck-ag',?,'MISSION','ACTIVE',NULL),
		       ('ck-g1','ck-ag',?,'CHAT','ACTIVE','AGENT')`,
		wsID, wsID, wsID, wsID, wsID, wsID, wsID)

	t.Run("totals every kind, not just the one fetched", func(t *testing.T) {
		rr := listChatsRaw(t, db, wsID, "direct&counts=1")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		// Order is AllChatKinds order, so the value is stable and diffable.
		if got, want := rr.Header().Get(ChatKindCountsHeader), "direct=2,routine=3,issue=1,agent=1"; got != want {
			t.Errorf("%s = %q, want %q", ChatKindCountsHeader, got, want)
		}
		// …and the BODY still holds only the kind that was asked for.
		var rows []chatResponse
		decodeChats(t, rr, &rows)
		if len(rows) != 2 {
			t.Errorf("body has %d rows, want the 2 direct ones", len(rows))
		}
	})

	t.Run("stays silent unless asked", func(t *testing.T) {
		// The count is over ALL of an agent's chats. `crewship chat list` has
		// no tab strip to fill and must not pay for it.
		rr := listChatsRaw(t, db, wsID, "direct")
		if got := rr.Header().Get(ChatKindCountsHeader); got != "" {
			t.Errorf("%s = %q on a request that did not ask for counts", ChatKindCountsHeader, got)
		}
	})

	t.Run("counts an agent with nothing as zeroes, not as absent", func(t *testing.T) {
		// Present-with-a-zero and missing are different answers: one says
		// "this agent has no routines", the other says "this server did not
		// say". The column draws them differently.
		execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('ck-crew2', ?, 'C2', 'ck-c2')`, wsID)
		seedAgentRow(t, db, "ck-ag2", wsID, "ck-crew2", "B", "ck-b", "AGENT")
		h := NewAgentHandler(db, newTestLogger())
		req := httptest.NewRequest("GET", "/api/v1/agents/ck-ag2/chats?counts=1", nil)
		req.SetPathValue("agentId", "ck-ag2")
		ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
		req = req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.ListChats(rr, req)
		if got, want := rr.Header().Get(ChatKindCountsHeader), "direct=0,routine=0,issue=0,agent=0"; got != want {
			t.Errorf("%s = %q, want %q", ChatKindCountsHeader, got, want)
		}
	})
}

func TestChatKindCountsAgreeWithTheFilter(t *testing.T) {
	// The counts fold GROUP BY (mode, origin) through ChatKindOf rather than
	// running one COUNT per predicate, so they cannot drift from the page they
	// sit beside. This is that promise, asserted: for every kind, the header's
	// number is exactly how many rows `?kind=<that>` returns.
	db := setupTestDB(t)
	wsID := chatKindSeed(t, db)
	origins := []any{nil, "UI", "CLI", "WEBHOOK", "CRON", "ROUTINE", "AGENT", "SOMETHING_NEW"}
	i := 0
	for _, mode := range []string{"CHAT", "MISSION", "TASK"} {
		for _, origin := range origins {
			i++
			execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status, origin)
				VALUES (?, 'ck-ag', ?, ?, 'ACTIVE', ?)`, fmt.Sprintf("ck-x%d", i), wsID, mode, origin)
		}
	}

	header := listChatsRaw(t, db, wsID, "direct&counts=1").Header().Get(ChatKindCountsHeader)
	fromHeader := map[string]int{}
	for _, pair := range strings.Split(header, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			t.Fatalf("unparseable pair %q in %q", pair, header)
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("unparseable count %q in %q", pair, header)
		}
		fromHeader[parts[0]] = n
	}

	for _, kind := range AllChatKinds {
		got := len(listChatIDs(t, db, wsID, string(kind)))
		if fromHeader[string(kind)] != got {
			t.Errorf("kind %s: header says %d, ?kind= returns %d",
				kind, fromHeader[string(kind)], got)
		}
	}
}
