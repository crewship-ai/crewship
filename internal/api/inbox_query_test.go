package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Server-side paging, search and filters for GET /api/v1/inbox and
// GET /api/v1/inbox/counts. The Harbor 2 Inbox used to download every page and
// filter in the browser; these tests pin the server doing it, and the counts
// endpoint agreeing with the list under the same parameters.

type inboxQuerySeed struct {
	id, kind, state, title, body string
	senderType, senderName       string
	attentionClass, payload      string
	blocking                     int
	age                          time.Duration // created this long before the fixed clock
}

var inboxQueryNow = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

func insertInboxQuerySeed(t *testing.T, h *InboxHandler, wsID string, s inboxQuerySeed) {
	t.Helper()
	at := inboxQueryNow.Add(-s.age).Format(time.RFC3339Nano)
	if s.payload == "" {
		s.payload = "{}"
	}
	_, err := h.db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md,
			sender_type, sender_id, sender_name, state, priority, blocking, attention_class, payload_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, 'medium', ?, NULLIF(?, ''), ?, ?, ?)`,
		s.id, wsID, s.kind, "src-"+s.id, s.title, s.body, s.senderType, s.senderName, s.state,
		s.blocking, s.attentionClass, s.payload, at, at)
	if err != nil {
		t.Fatalf("seed %s: %v", s.id, err)
	}
}

type inboxQueryList struct {
	Rows []struct {
		ID string `json:"id"`
	} `json:"rows"`
	Count      int    `json:"count"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

func (l inboxQueryList) ids() []string {
	out := make([]string, 0, len(l.Rows))
	for _, r := range l.Rows {
		out = append(out, r.ID)
	}
	return out
}

func inboxQueryGet(t *testing.T, h *InboxHandler, userID, wsID, path string, params url.Values) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("GET", path+"?"+params.Encode(), nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	if path == "/api/v1/inbox/counts" {
		h.Counts(rr, req)
	} else {
		h.List(rr, req)
	}
	return rr.Code, rr.Body.Bytes()
}

func inboxQueryFixture(t *testing.T) (*InboxHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	in := func(d time.Duration) string { return inboxQueryNow.Add(d).Format(time.RFC3339) }
	seeds := []inboxQuerySeed{
		{id: "a1", kind: "waitpoint", state: "unread", title: "Approve deploy", senderType: "system", senderName: "Keeper", age: 1 * time.Minute,
			payload: `{"agent_name":"Riley","agent_slug":"riley","crew_id":"crew-1","timeout_at":"` + in(30*time.Minute) + `"}`},
		{id: "a2", kind: "waitpoint", state: "unread", title: "Approve spend", senderType: "system", senderName: "Keeper", age: 2 * time.Minute,
			payload: `{"timeout_at":"` + in(5*time.Hour) + `"}`},
		{id: "a3", kind: "waitpoint", state: "unread", title: "Approve hire", senderType: "system", senderName: "Keeper", age: 3 * time.Minute,
			payload: `{"timeout_at":"` + in(48*time.Hour) + `"}`},
		{id: "a4", kind: "escalation", state: "unread", title: "Need a token", senderType: "agent", senderName: "casey", age: 4 * time.Minute,
			payload: `{"escalation_type":"TEXT"}`},
		{id: "a5", kind: "escalation", state: "unread", title: "Skill proposal", senderType: "system", senderName: "Skill Curator", age: 5 * time.Minute,
			payload: `{"kind":"skill_proposal","invoking_crew_id":"crew-2"}`},
		{id: "a6", kind: "failed_run", state: "unread", title: "Run failed: nightly", body: "The disk is full", senderType: "system", senderName: "Runs", age: 6 * time.Minute},
		{id: "a7", kind: "message", state: "unread", title: "Hello there", senderType: "system", senderName: "Keeper", age: 7 * time.Minute},
		{id: "a8", kind: "escalation", state: "unread", title: "Advisory 100% done", senderType: "system", senderName: "Keeper", blocking: 1, age: 8 * time.Minute},
		{id: "a9", kind: "message", state: "unread", title: "Review me", senderType: "system", senderName: "Keeper", attentionClass: "review", age: 9 * time.Minute},
		{id: "h1", kind: "failed_run", state: "resolved", title: "Old failure", senderType: "system", senderName: "Runs", age: 10 * time.Minute},
	}
	for _, s := range seeds {
		insertInboxQuerySeed(t, h, wsID, s)
	}
	// a6 is read for this caller: still in the list and the totals, not unread.
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES ('a6', ?)`, userID); err != nil {
		t.Fatal(err)
	}
	return h, userID, wsID
}

func TestInboxHandler_List_ServerFilters(t *testing.T) {
	h, userID, wsID := inboxQueryFixture(t)
	now := inboxQueryNow.Format(time.RFC3339)
	dayEnd := inboxQueryNow.Add(10 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name   string
		params url.Values
		want   []string
	}{
		{"search title", url.Values{"q": {"deploy"}}, []string{"a1"}},
		{"search is case-insensitive", url.Values{"q": {"DEPLOY"}}, []string{"a1"}},
		{"search body", url.Values{"q": {"disk"}}, []string{"a6"}},
		{"search sender", url.Values{"q": {"curator"}}, []string{"a5"}},
		{"search payload agent", url.Values{"q": {"riley"}}, []string{"a1"}},
		{"search kind", url.Values{"q": {"failed_run"}}, []string{"a6", "h1"}},
		{"search trims", url.Values{"q": {"  hire  "}}, []string{"a3"}},
		{"search percent is literal", url.Values{"q": {"100%"}}, []string{"a8"}},
		{"search underscore is literal", url.Values{"q": {"_"}}, []string{"a6", "h1"}},
		{"search no match", url.Values{"q": {"zzzz"}}, []string{}},
		{"space action", url.Values{"space": {"action"}}, []string{"a1", "a2", "a3", "a4", "a5"}},
		{"space updates", url.Values{"space": {"updates"}}, []string{"a6", "a7", "a8", "a9"}},
		{"space history", url.Values{"space": {"history"}}, []string{"h1"}},
		{"unread only", url.Values{"unread": {"true"}}, []string{"a1", "a2", "a3", "a4", "a5", "a7", "a8", "a9"}},
		{"attention decision", url.Values{"attention": {"decision"}}, []string{"a1", "a2", "a3"}},
		{"attention input", url.Values{"attention": {"input"}}, []string{"a4"}},
		{"attention review", url.Values{"attention": {"review"}}, []string{"a5", "a9"}},
		{"attention repair", url.Values{"attention": {"repair"}}, []string{"a6"}},
		{"deadline hour", url.Values{"deadline": {"hour"}, "now": {now}, "day_end": {dayEnd}}, []string{"a1"}},
		{"deadline today", url.Values{"deadline": {"today"}, "now": {now}, "day_end": {dayEnd}}, []string{"a2"}},
		{"deadline soon", url.Values{"deadline": {"soon"}, "now": {now}, "day_end": {dayEnd}}, []string{"a1", "a2"}},
		{"deadline none", url.Values{"deadline": {"none"}, "now": {now}, "day_end": {dayEnd}}, []string{"a4", "a5", "a6", "a7", "a8", "a9", "h1"}},
		{"crew from crew_id", url.Values{"crew": {"crew-1"}}, []string{"a1"}},
		{"crew from invoking_crew_id", url.Values{"crew": {"crew-2"}}, []string{"a5"}},
		{"agent by slug", url.Values{"agent": {"riley"}}, []string{"a1"}},
		{"agent by name", url.Values{"agent": {"Riley"}}, []string{"a1"}},
		{"agent by sender", url.Values{"agent": {"casey"}}, []string{"a4"}},
		{"kind", url.Values{"kind": {"message"}}, []string{"a7", "a9"}},
		{"filters combine", url.Values{"space": {"action"}, "q": {"approve"}, "unread": {"true"}}, []string{"a1", "a2", "a3"}},
		{"state active excludes history", url.Values{"state": {"active"}, "kind": {"failed_run"}}, []string{"a6"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := url.Values{"limit": {"500"}}
			for k, v := range c.params {
				params[k] = v
			}
			code, body := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", params)
			if code != http.StatusOK {
				t.Fatalf("status %d: %s", code, body)
			}
			var list inboxQueryList
			if err := json.Unmarshal(body, &list); err != nil {
				t.Fatal(err)
			}
			got := list.ids()
			sort.Strings(got)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rows = %v, want %v", got, c.want)
			}

			// The counts endpoint answers the same question with the same
			// parameters: its total is the number of rows the list returns.
			params.Del("limit")
			params.Del("state")
			if c.params.Get("state") == "" {
				code, body = inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox/counts", params)
				if code != http.StatusOK {
					t.Fatalf("counts status %d: %s", code, body)
				}
				var counts struct {
					Total int `json:"total"`
				}
				if err := json.Unmarshal(body, &counts); err != nil {
					t.Fatal(err)
				}
				if counts.Total != len(c.want) {
					t.Errorf("counts total = %d, list returned %d", counts.Total, len(c.want))
				}
			}
		})
	}
}

func TestInboxHandler_ServerFilters_RejectBadValues(t *testing.T) {
	h, userID, wsID := inboxQueryFixture(t)
	for _, c := range []url.Values{
		{"space": {"everything"}},
		{"attention": {"urgent"}},
		{"deadline": {"yesterday"}},
		{"unread": {"maybe"}},
		{"now": {"not-a-time"}},
		{"day_end": {"not-a-time"}},
		{"cursor": {"!!not-base64!!"}},
		{"cursor": {"bm9wZQ"}}, // decodes, but is not a cursor we issued
	} {
		for _, path := range []string{"/api/v1/inbox", "/api/v1/inbox/counts"} {
			if path == "/api/v1/inbox/counts" && c.Get("cursor") != "" {
				continue // counts has no paging
			}
			code, body := inboxQueryGet(t, h, userID, wsID, path, c)
			if code != http.StatusBadRequest {
				t.Errorf("%s %v: status %d (%s), want 400", path, c, code, body)
			}
		}
	}
	// A cursor and an offset are two ways to say where to start; refuse both.
	code, _ := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"limit": {"2"}})
	if code != http.StatusOK {
		t.Fatal(code)
	}
	_, body := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"limit": {"2"}})
	var first inboxQueryList
	_ = json.Unmarshal(body, &first)
	code, _ = inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"cursor": {first.NextCursor}, "offset": {"1"}})
	if code != http.StatusBadRequest {
		t.Errorf("cursor+offset status = %d, want 400", code)
	}
}

func TestInboxHandler_Counts_FacetBreakdowns(t *testing.T) {
	h, userID, wsID := inboxQueryFixture(t)
	now := inboxQueryNow.Format(time.RFC3339)
	dayEnd := inboxQueryNow.Add(10 * time.Hour).Format(time.RFC3339)
	code, body := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox/counts", url.Values{"now": {now}, "day_end": {dayEnd}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var got struct {
		Total       int            `json:"total"`
		ByAttention map[string]int `json:"by_attention"`
		ByDeadline  map[string]int `json:"by_deadline"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	wantAttention := map[string]int{"decision": 3, "input": 1, "review": 2, "repair": 1}
	wantDeadline := map[string]int{"hour": 1, "today": 1, "none": 7, "soon": 2}
	if got.Total != 10 || !reflect.DeepEqual(got.ByAttention, wantAttention) || !reflect.DeepEqual(got.ByDeadline, wantDeadline) {
		t.Fatalf("total=%d by_attention=%v by_deadline=%v\nwant 10 %v %v", got.Total, got.ByAttention, got.ByDeadline, wantAttention, wantDeadline)
	}
}

// Keyset paging: the feed is walked newest first, a row inserted mid-walk
// neither shifts the window nor repeats a row, and rows that share a
// timestamp are not skipped at a page boundary.
func TestInboxHandler_List_CursorPaging(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	var want []string
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("tie-%d", i)
		insertInboxQuerySeed(t, h, wsID, inboxQuerySeed{id: id, kind: "message", state: "unread", title: id, senderType: "system", age: time.Hour})
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("old-%d", i)
		insertInboxQuerySeed(t, h, wsID, inboxQuerySeed{id: id, kind: "message", state: "unread", title: id, senderType: "system", age: time.Duration(2+i) * time.Hour})
	}
	// Reference order: the unpaged list.
	code, body := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"limit": {"500"}})
	if code != http.StatusOK {
		t.Fatal(code, string(body))
	}
	var full inboxQueryList
	_ = json.Unmarshal(body, &full)
	want = full.ids()
	if len(want) != 12 || full.NextCursor != "" || full.HasMore {
		t.Fatalf("unpaged list: %d rows, next_cursor=%q has_more=%v", len(want), full.NextCursor, full.HasMore)
	}

	var got []string
	cursor := ""
	pages := 0
	for {
		params := url.Values{"limit": {"5"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		code, body := inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", params)
		if code != http.StatusOK {
			t.Fatal(code, string(body))
		}
		var page inboxQueryList
		_ = json.Unmarshal(body, &page)
		got = append(got, page.ids()...)
		pages++
		if pages == 1 {
			// A newer row arrives between page one and page two.
			insertInboxQuerySeed(t, h, wsID, inboxQuerySeed{id: "newcomer", kind: "message", state: "unread", title: "n", senderType: "system", age: 0})
		}
		if page.HasMore != (page.NextCursor != "") {
			t.Fatalf("page %d: has_more=%v but next_cursor=%q", pages, page.HasMore, page.NextCursor)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("cursor never ends")
		}
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3 (5+5+2)", pages)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk = %v\nwant %v", got, want)
	}

	// The cursor composes with a filter.
	_, body = inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"limit": {"3"}, "q": {"old-"}})
	var p1 inboxQueryList
	_ = json.Unmarshal(body, &p1)
	_, body = inboxQueryGet(t, h, userID, wsID, "/api/v1/inbox", url.Values{"limit": {"3"}, "q": {"old-"}, "cursor": {p1.NextCursor}})
	var p2 inboxQueryList
	_ = json.Unmarshal(body, &p2)
	if !reflect.DeepEqual(append(p1.ids(), p2.ids()...), []string{"old-0", "old-1", "old-2", "old-3", "old-4"}) || p2.NextCursor != "" {
		t.Errorf("filtered walk = %v + %v (next %q)", p1.ids(), p2.ids(), p2.NextCursor)
	}
}
