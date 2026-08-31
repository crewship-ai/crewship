package chatkind

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// =============================================================================
// The partition lives here, so it is pinned here.
//
// Two packages now read this rule and neither owns it: internal/api pages the
// conversations column by it, internal/chatnotify decides by it whether a reply
// earns a place in an inbox. Testing it only through internal/api's handlers —
// which is where it was tested when it was a file in that package — leaves the
// notifier depending on behaviour no test in its own dependency chain asserts.
// A change here that internal/api happens not to page on would ship green and
// surface as a bell for a routine step.
//
// The load-bearing invariant is the one TestPredicatesMatchClassifier asserts:
// Of() and Predicates are two implementations of one rule, in two languages,
// and they must agree on every row the column can hold.
// =============================================================================

// Every combination the column can hold, including the ones no writer produces
// today. A restored backup and an origin value invented next quarter are both
// rows this code will meet.
var (
	testModes   = []string{"CHAT", "MISSION", "TASK"}
	testOrigins = []string{"", "UI", "CLI", "WEBHOOK", "CRON", "ROUTINE", "AGENT", "SOMETHING_NEW"}
)

func TestOf(t *testing.T) {
	// Spelled out rather than derived, so this table is a specification and
	// not a second copy of the switch it is checking.
	cases := []struct {
		mode   string
		origin string
		want   Kind
	}{
		{"CHAT", "", Direct},
		{"CHAT", "UI", Direct},
		{"CHAT", "CLI", Direct},
		{"CHAT", "WEBHOOK", Routine},
		{"CHAT", "CRON", Routine},
		{"CHAT", "ROUTINE", Routine},
		{"CHAT", "AGENT", Agent},
		// An origin nobody has thought of yet is Direct, not a fifth bucket
		// and not nothing. The catch-all is the point of the package: a future
		// value stays VISIBLE in the default column instead of belonging to no
		// kind and vanishing from every surface at once.
		{"CHAT", "SOMETHING_NEW", Direct},

		// Mode wins over origin. A MISSION dispatched by cron is an issue
		// doing work, and it belongs on the issue, not in /routines — the
		// origin says who pressed go, the mode says what is running.
		{"MISSION", "", Issue},
		{"MISSION", "UI", Issue},
		{"MISSION", "CLI", Issue},
		{"MISSION", "WEBHOOK", Issue},
		{"MISSION", "CRON", Issue},
		{"MISSION", "ROUTINE", Issue},
		{"MISSION", "AGENT", Issue},
		{"MISSION", "SOMETHING_NEW", Issue},

		// TASK is not MISSION, so it classifies purely on origin. An
		// unrecognised mode must not become a fifth kind either.
		{"TASK", "", Direct},
		{"TASK", "UI", Direct},
		{"TASK", "CLI", Direct},
		{"TASK", "WEBHOOK", Routine},
		{"TASK", "CRON", Routine},
		{"TASK", "ROUTINE", Routine},
		{"TASK", "AGENT", Agent},
		{"TASK", "SOMETHING_NEW", Direct},
	}

	// The table must stay exhaustive over the matrix, or a combination could
	// be dropped from it and nothing would say so.
	if len(cases) != len(testModes)*len(testOrigins) {
		t.Fatalf("table has %d cases, want %d (every mode × every origin)",
			len(cases), len(testModes)*len(testOrigins))
	}

	for _, tc := range cases {
		t.Run(tc.mode+"/"+originLabel(tc.origin), func(t *testing.T) {
			if got := Of(tc.mode, tc.origin); got != tc.want {
				t.Errorf("Of(%q, %q) = %q, want %q", tc.mode, tc.origin, got, tc.want)
			}
		})
	}
}

func TestOfAlwaysReturnsAKnownKind(t *testing.T) {
	// Whatever Of answers must be a kind the rest of the package can act on:
	// a Kind outside All has no predicate, no count slot and no filter value,
	// so it would be a row the API can name but never select.
	known := map[Kind]bool{}
	for _, k := range All {
		known[k] = true
	}
	for _, mode := range append(testModes, "", "mission", "WHATEVER") {
		for _, origin := range append(testOrigins, "routine", "Agent") {
			if k := Of(mode, origin); !known[k] {
				t.Errorf("Of(%q, %q) = %q, which is not in All", mode, origin, k)
			}
		}
	}
}

func TestPredicatesCoverExactlyAll(t *testing.T) {
	// A kind added to the vocabulary without SQL to select it would give the
	// UI a tab that can only ever be empty; a predicate with no kind would be
	// a bucket nothing routes to. ParseFilter's "every kind collapses to no
	// filter" shortcut compares against len(Predicates), so the two lists
	// disagreeing also silently changes what `?kind=direct,routine,issue,agent`
	// means.
	if len(Predicates) != len(All) {
		t.Errorf("len(Predicates) = %d, len(All) = %d", len(Predicates), len(All))
	}
	for _, k := range All {
		if _, ok := Predicates[k]; !ok {
			t.Errorf("kind %q is in All but has no predicate", k)
		}
	}
	inAll := map[Kind]bool{}
	for _, k := range All {
		inAll[k] = true
	}
	for k := range Predicates {
		if !inAll[k] {
			t.Errorf("kind %q has a predicate but is not in All", k)
		}
	}
}

func TestPredicatesMatchClassifier(t *testing.T) {
	// The most important test in the package. Of() is Go, Predicates is SQL,
	// and they are the same rule written twice — the server pages rows with
	// one and labels them with the other, so drift means a row arriving in the
	// Routines list wearing a Direct badge.
	db := openChatsDB(t)
	want := seedMatrix(t, db, "ws-1")

	matched := map[string]int{}
	for kind, pred := range Predicates {
		for _, id := range selectIDs(t, db, `SELECT c.id FROM chats c WHERE c.workspace_id = 'ws-1' AND (`+pred+`)`) {
			matched[id]++
			if want[id] != kind {
				t.Errorf("row %s: SQL says %q, Of says %q", id, kind, want[id])
			}
		}
	}

	// Total AND disjoint, and both halves fail differently.
	//
	// Matched by nothing: the row exists, is charged to the agent, and is
	// returned by no scope — unfindable, with nothing on screen to say it was
	// left out. That is the failure the negated Direct predicate exists to
	// make impossible.
	//
	// Matched twice: the row shows up in two lists, the per-kind counts add up
	// to more than the table holds, and whichever tab you are standing in
	// looks correct in isolation.
	for id := range want {
		if matched[id] != 1 {
			t.Errorf("row %s matched %d predicates, want exactly 1", id, matched[id])
		}
	}
}

func TestParseFilter(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantAll  bool // no narrowing at all
		wantErr  bool
		contains []string
	}{
		// Absent and "all" are the pre-parameter behaviour byte for byte, so
		// every caller written before `?kind=` existed keeps its answer.
		{name: "absent", raw: "", wantAll: true},
		{name: "explicit all", raw: "all", wantAll: true},
		{name: "whitespace only", raw: "   ", wantAll: true},
		// Every kind at once is the same set of rows as no filter, and asking
		// SQLite to evaluate a four-branch tautology per row buys nothing.
		{name: "every kind collapses", raw: "direct,routine,issue,agent", wantAll: true},
		{name: "every kind, any order", raw: "agent,issue,routine,direct", wantAll: true},
		{name: "duplicates collapse", raw: "issue,issue", contains: []string{Predicates[Issue]}},

		{name: "one kind", raw: "direct", contains: []string{Predicates[Direct]}},
		{name: "one kind: routine", raw: "routine", contains: []string{Predicates[Routine]}},
		{name: "two kinds", raw: "routine,issue", contains: []string{" OR ", Predicates[Routine], Predicates[Issue]}},
		// Case and padding come from a hand-typed CLI flag and a URL a person
		// edited; neither is a typo the user should have to see an error for.
		{name: "case and spacing", raw: " Routine , ISSUE ", contains: []string{" OR ", Predicates[Routine], Predicates[Issue]}},
		// A trailing comma is what a shell loop that appends ",$kind" leaves
		// behind. The empty element is skipped rather than rejected.
		{name: "trailing comma", raw: "direct,", contains: []string{Predicates[Direct]}},

		// A typo must not quietly widen the list back to everything — that is
		// the mixed column the parameter exists to prevent, arriving through
		// the front door and looking like success.
		{name: "unknown kind", raw: "routines", wantErr: true},
		{name: "unknown among known", raw: "direct,nonsense", wantErr: true},
		{name: "plural of a real kind", raw: "issues", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			where, err := ParseFilter(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseFilter(%q) = %q, want error", tc.raw, where)
				}
				// The error has to teach the vocabulary, not just refuse: the
				// caller is a person at a CLI with no list in front of them.
				if !strings.Contains(err.Error(), "direct") {
					t.Errorf("error should name the vocabulary, got %q", err)
				}
				if where != "" {
					t.Errorf("ParseFilter(%q) returned a fragment %q alongside an error", tc.raw, where)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFilter(%q): %v", tc.raw, err)
			}
			if tc.wantAll && where != "" {
				t.Fatalf("ParseFilter(%q) = %q, want no narrowing", tc.raw, where)
			}
			if !tc.wantAll && where == "" {
				t.Fatalf("ParseFilter(%q) narrowed nothing", tc.raw)
			}
			for _, want := range tc.contains {
				if !strings.Contains(where, want) {
					t.Errorf("ParseFilter(%q) = %q, want it to contain %q", tc.raw, where, want)
				}
			}
		})
	}
}

func TestParseFilterRefusesAValueWithNoKindInIt(t *testing.T) {
	// A value that is nothing but separators used to parse as "all": every
	// element hit the empty-string skip, `seen` came out empty, and the
	// len(seen)==0 branch returned NO NARROWING. The caller asked to narrow
	// and got the unfiltered column back with no error to say the request had
	// been dropped — the mixed column `?kind=` exists to prevent, reachable
	// from any client that joins an empty selection with commas.
	//
	// Failing open is the wrong direction for this parameter specifically: the
	// list it guards is the one a routine can bury, so "I could not understand
	// you" has to be louder than "here is everything".
	for _, raw := range []string{",", " , ", ",,,"} {
		where, err := ParseFilter(raw)
		if err == nil {
			t.Errorf("ParseFilter(%q) = %q, want an error", raw, where)
			continue
		}
		if where != "" {
			t.Errorf("ParseFilter(%q) returned a fragment %q alongside its error", raw, where)
		}
		if !strings.Contains(err.Error(), "direct") {
			t.Errorf("ParseFilter(%q) error %q should name the vocabulary", raw, err)
		}
	}
}

func TestParseFilterForgivesAStraySeparator(t *testing.T) {
	// The other half of the rule above, and the reason it is not simply
	// "reject anything with an empty element": `--kind "direct,"` is what a
	// shell loop appending ",$kind" produces, and refusing it would be
	// pedantry about a value whose meaning is unambiguous. Only a value with
	// NO kind in it at all is an error.
	for _, raw := range []string{"direct,", ",direct", "direct,,routine"} {
		where, err := ParseFilter(raw)
		if err != nil {
			t.Errorf("ParseFilter(%q): %v", raw, err)
			continue
		}
		if where == "" {
			t.Errorf("ParseFilter(%q) = no narrowing, want a fragment", raw)
		}
	}
}

func TestParseFilterTreatsAllLikeEveryOtherWord(t *testing.T) {
	// "all" used to be matched against the raw string BEFORE the loop that
	// lowercases each element, which made it the one word in the vocabulary
	// that cared about case: every kind tolerated "ROUTINE" and only "all" did
	// not. `--kind ALL` answered
	//
	//	unknown kind "ALL" (want one of direct, routine, issue, agent, all)
	//
	// — an error offering the word it had just refused, to somebody standing
	// at a terminal.
	for _, raw := range []string{"all", "ALL", "All", " all ", "\tALL\n"} {
		where, err := ParseFilter(raw)
		if err != nil {
			t.Errorf("ParseFilter(%q): %v", raw, err)
			continue
		}
		if where != "" {
			t.Errorf("ParseFilter(%q) = %q, want no narrowing", raw, where)
		}
	}
}

func TestParseFilterRefusesAllAlongsideAKind(t *testing.T) {
	// Naming "all" next to a kind is a contradiction, not a refinement, and
	// the dangerous reading is the generous one: answering it with the union
	// hands back the mixed column the parameter exists to avoid, while the
	// caller believes they narrowed. The error says what is wrong with the
	// combination rather than pretending the word is unknown.
	for _, raw := range []string{"all,direct", "direct,all", "ALL,routine"} {
		where, err := ParseFilter(raw)
		if err == nil {
			t.Errorf("ParseFilter(%q) = %q, want an error", raw, where)
			continue
		}
		if !strings.Contains(err.Error(), "combined") {
			t.Errorf("ParseFilter(%q) error %q should say the two cannot be combined", raw, err)
		}
	}
}

func TestParseFilterIsStable(t *testing.T) {
	// Same question, same statement text. An unstable one gives every
	// permutation of the parameter its own entry in the prepared-statement
	// cache, for a set of rows that is identical either way.
	a, errA := ParseFilter("issue,routine")
	b, errB := ParseFilter("routine,issue")
	if errA != nil || errB != nil {
		t.Fatalf("ParseFilter errored: %v / %v", errA, errB)
	}
	if a != b {
		t.Errorf("order of the parameter changed the SQL:\n %q\n %q", a, b)
	}
	// And stable across calls, not merely between two orderings — a map
	// iteration leaking into the output would pass the check above roughly
	// half the time.
	for i := 0; i < 20; i++ {
		if got, _ := ParseFilter("agent,direct,issue"); got != mustParse(t, "agent,direct,issue") {
			t.Fatalf("ParseFilter is not deterministic: %q", got)
		}
	}
}

func TestParseFilterFragmentIsValidSQL(t *testing.T) {
	// The fragment is concatenated after a real WHERE by its callers, so
	// string-matching it proves nothing about whether it parses. Run it.
	// Every combination is in the table, so this also re-checks that a filter
	// selects exactly the rows Of assigns to the kinds asked for.
	db := openChatsDB(t)
	want := seedMatrix(t, db, "ws-1")

	cases := []struct {
		raw   string
		kinds []Kind
	}{
		{"direct", []Kind{Direct}},
		{"routine", []Kind{Routine}},
		{"issue", []Kind{Issue}},
		{"agent", []Kind{Agent}},
		{"routine,issue", []Kind{Routine, Issue}},
		{" Agent , DIRECT ", []Kind{Agent, Direct}},
		{"direct,routine,issue,agent", All},
		{"all", All},
		{"", All},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			frag, err := ParseFilter(tc.raw)
			if err != nil {
				t.Fatalf("ParseFilter(%q): %v", tc.raw, err)
			}
			got := selectIDs(t, db, `SELECT c.id FROM chats c WHERE c.workspace_id = 'ws-1'`+frag)

			wantIDs := []string{}
			for id, k := range want {
				for _, ask := range tc.kinds {
					if k == ask {
						wantIDs = append(wantIDs, id)
					}
				}
			}
			sort.Strings(got)
			sort.Strings(wantIDs)
			if strings.Join(got, " ") != strings.Join(wantIDs, " ") {
				t.Errorf("kind=%q selected\n %v\nwant\n %v", tc.raw, got, wantIDs)
			}
		})
	}
}

func TestIsMachine(t *testing.T) {
	// The question every notification path actually has, named once so each of
	// them does not answer it with its own list of origins. Direct is the only
	// kind a person opened; a bell about any of the other three is a bell about
	// work the reader never asked to be told about.
	cases := map[Kind]bool{
		Direct:  false,
		Routine: true,
		Issue:   true,
		Agent:   true,
	}
	for _, k := range All {
		want, ok := cases[k]
		if !ok {
			t.Fatalf("kind %q is in All but this test has no expectation for it", k)
		}
		if got := IsMachine(k); got != want {
			t.Errorf("IsMachine(%q) = %v, want %v", k, got, want)
		}
	}
	// Exactly one kind is a conversation. If a second one ever is, every
	// notification path changes meaning and should have to say so here first.
	human := 0
	for _, k := range All {
		if !IsMachine(k) {
			human++
		}
	}
	if human != 1 {
		t.Errorf("%d kinds are human-opened, want exactly 1 (Direct)", human)
	}
}

func TestIsOrigin(t *testing.T) {
	for _, v := range OriginValues {
		if !IsOrigin(v) {
			t.Errorf("IsOrigin(%q) = false, but it is in OriginValues", v)
		}
	}
	// "" is how an unstamped chat is stored, and it must not read back as a
	// storable origin — otherwise a NULL round-trips into the whitelist and
	// the write side stops distinguishing "not stated" from "stated".
	for _, v := range []string{"", " ", "ui", "Routine", "SOMETHING_NEW", "MISSION"} {
		if IsOrigin(v) {
			t.Errorf("IsOrigin(%q) = true, want false", v)
		}
	}
}

func TestOriginValuesIncludesRoutine(t *testing.T) {
	// ROUTINE is the newest member and the one the pipeline runner stamps.
	// Dropping it from the whitelist does not fail a build: the endpoint just
	// starts storing NULL, and every routine step goes back to being
	// indistinguishable from a thread somebody opened by hand.
	if !IsOrigin(OriginRoutine) {
		t.Fatalf("OriginValues %v does not contain %q", OriginValues, OriginRoutine)
	}
	if OriginRoutine != "ROUTINE" {
		t.Errorf("OriginRoutine = %q, want %q — it is a stored column value, not a label", OriginRoutine, "ROUTINE")
	}
	// Every whitelisted origin must classify. CRON and WEBHOOK predate ROUTINE
	// and mean the same thing to a reader, so all three land in Routine.
	for _, tc := range []struct {
		origin string
		want   Kind
	}{
		{"UI", Direct}, {"CLI", Direct},
		{"WEBHOOK", Routine}, {"CRON", Routine}, {OriginRoutine, Routine},
		{"AGENT", Agent},
	} {
		if got := Of("CHAT", tc.origin); got != tc.want {
			t.Errorf("Of(CHAT, %q) = %q, want %q", tc.origin, got, tc.want)
		}
	}
}

func TestFormatCounts(t *testing.T) {
	// Rendered in All order so the header is stable across requests and
	// diffable in a log, and every kind is present even at zero:
	// present-with-a-zero says "this agent has no routines", absent says "this
	// server did not say", and the column draws those differently.
	tests := []struct {
		name   string
		counts map[Kind]int
		want   string
	}{
		{
			name:   "a mix, including a zero",
			counts: map[Kind]int{Direct: 3, Routine: 182, Agent: 1},
			want:   "direct=3,routine=182,issue=0,agent=1",
		},
		{
			name:   "an agent with nothing",
			counts: map[Kind]int{},
			want:   "direct=0,routine=0,issue=0,agent=0",
		},
		{
			name:   "nil map is an agent with nothing, not a panic",
			counts: nil,
			want:   "direct=0,routine=0,issue=0,agent=0",
		},
		{
			name:   "insertion order of the map does not leak",
			counts: map[Kind]int{Agent: 4, Issue: 3, Routine: 2, Direct: 1},
			want:   "direct=1,routine=2,issue=3,agent=4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				if got := FormatCounts(tc.counts); got != tc.want {
					t.Fatalf("FormatCounts = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestCountByAgent(t *testing.T) {
	db := openChatsDB(t)
	// Two workspaces and two agents, so both scopes are actually load-bearing
	// rather than trivially satisfied by the only rows in the table.
	insert(t, db, "d1", "ag-1", "ws-1", "CHAT", "UI")
	insert(t, db, "d2", "ag-1", "ws-1", "CHAT", "")
	insert(t, db, "d3", "ag-1", "ws-1", "TASK", "SOMETHING_NEW")
	insert(t, db, "r1", "ag-1", "ws-1", "CHAT", "ROUTINE")
	insert(t, db, "r2", "ag-1", "ws-1", "CHAT", "CRON")
	insert(t, db, "r3", "ag-1", "ws-1", "CHAT", "WEBHOOK")
	insert(t, db, "i1", "ag-1", "ws-1", "MISSION", "")
	// A MISSION stamped ROUTINE counts once, as an issue. If the totals were
	// one COUNT per predicate instead of one fold through Of, this is the row
	// that would be counted twice.
	insert(t, db, "i2", "ag-1", "ws-1", "MISSION", "ROUTINE")
	insert(t, db, "g1", "ag-1", "ws-1", "CHAT", "AGENT")
	// Another agent in the same workspace, and the same agent id in another
	// workspace. Neither may be counted.
	insert(t, db, "other-agent", "ag-2", "ws-1", "CHAT", "UI")
	insert(t, db, "other-ws", "ag-1", "ws-2", "CHAT", "UI")

	got, err := CountByAgent(context.Background(), db, "ag-1", "ws-1")
	if err != nil {
		t.Fatalf("CountByAgent: %v", err)
	}
	want := map[Kind]int{Direct: 3, Routine: 3, Issue: 2, Agent: 1}
	for _, k := range All {
		if got[k] != want[k] {
			t.Errorf("count[%s] = %d, want %d (full result %v)", k, got[k], want[k], got)
		}
	}
	// Every kind is a key even when it is zero — a caller cannot tell "no
	// routines" from "no answer" if the key is simply missing.
	empty, err := CountByAgent(context.Background(), db, "ag-nothing", "ws-1")
	if err != nil {
		t.Fatalf("CountByAgent: %v", err)
	}
	if len(empty) != len(All) {
		t.Errorf("an agent with no chats returned %d keys, want %d", len(empty), len(All))
	}
	for _, k := range All {
		if n, ok := empty[k]; !ok || n != 0 {
			t.Errorf("empty count[%s] = %d (present=%v), want 0 and present", k, n, ok)
		}
	}
}

func TestCountByAgentAgreesWithTheFilter(t *testing.T) {
	// The totals fold GROUP BY (mode, origin) through Of; the page beside them
	// is cut by Predicates. This asserts the promise that makes the fold worth
	// it: for every kind, the count is exactly how many rows that kind's
	// predicate selects. A number on a bucket is the last place anyone would
	// notice the two had drifted.
	db := openChatsDB(t)
	seedMatrix(t, db, "ws-1")
	counts, err := CountByAgent(context.Background(), db, "ag-1", "ws-1")
	if err != nil {
		t.Fatalf("CountByAgent: %v", err)
	}
	total := 0
	for _, k := range All {
		n := len(selectIDs(t, db, `SELECT c.id FROM chats c WHERE c.workspace_id = 'ws-1' AND (`+Predicates[k]+`)`))
		if counts[k] != n {
			t.Errorf("kind %s: CountByAgent says %d, its predicate selects %d", k, counts[k], n)
		}
		total += counts[k]
	}
	if want := len(testModes) * len(testOrigins); total != want {
		t.Errorf("counts total %d, but %d rows were inserted", total, want)
	}
}

func TestOfChat(t *testing.T) {
	db := openChatsDB(t)
	insert(t, db, "c-direct", "ag-1", "ws-1", "CHAT", "UI")
	insert(t, db, "c-null", "ag-1", "ws-1", "CHAT", "")
	insert(t, db, "c-routine", "ag-1", "ws-1", "CHAT", "ROUTINE")
	insert(t, db, "c-issue", "ag-1", "ws-1", "MISSION", "CRON")
	insert(t, db, "c-agent", "ag-1", "ws-1", "TASK", "AGENT")
	insert(t, db, "c-elsewhere", "ag-1", "ws-2", "CHAT", "ROUTINE")

	for id, want := range map[string]Kind{
		"c-direct":  Direct,
		"c-null":    Direct,
		"c-routine": Routine,
		"c-issue":   Issue,
		"c-agent":   Agent,
	} {
		got, err := OfChat(context.Background(), db, id, "ws-1")
		if err != nil {
			t.Fatalf("OfChat(%s): %v", id, err)
		}
		if got != want {
			t.Errorf("OfChat(%s) = %q, want %q", id, got, want)
		}
	}

	// sql.ErrNoRows is also the caller's tenant check: a chat that exists but
	// belongs to another workspace must be indistinguishable from one that
	// does not exist at all. Anything softer — a zero Kind and a nil error —
	// would let a caller reason about a row it may not see, and would arrive
	// at the notifier as Direct, which is the one kind that rings a bell.
	for _, tc := range []struct{ name, chat, ws string }{
		{"no such chat", "c-missing", "ws-1"},
		{"chat in another workspace", "c-elsewhere", "ws-1"},
		{"right chat, wrong workspace", "c-direct", "ws-2"},
		{"empty workspace", "c-direct", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OfChat(context.Background(), db, tc.chat, tc.ws)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("OfChat(%q, %q) = (%q, %v), want sql.ErrNoRows", tc.chat, tc.ws, got, err)
			}
			if got != "" {
				t.Errorf("OfChat returned kind %q alongside an error", got)
			}
		})
	}
}

func TestList(t *testing.T) {
	// What an error message hands a person who mistyped. It must name every
	// kind plus "all", because "all" is the only way back to the unfiltered
	// column and is not itself a kind.
	got := List()
	for _, k := range All {
		if !strings.Contains(got, string(k)) {
			t.Errorf("List() = %q, missing kind %q", got, k)
		}
	}
	if !strings.Contains(got, "all") {
		t.Errorf("List() = %q, missing \"all\"", got)
	}
}

/* ------------------------------------------------------------------ helpers */

// openChatsDB builds the smallest table the package's SQL touches. No FKs and
// no real schema on purpose: this package's contract is four columns, and a
// test that needs the migrations to run is a test that fails for reasons that
// have nothing to do with the partition.
//
// `mode` is NOT NULL here because it is NOT NULL in the real schema, and the
// predicates depend on that: `c.mode <> 'MISSION'` is NULL — neither true nor
// false — for a NULL mode, so such a row would match no predicate at all.
func openChatsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE chats (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		mode TEXT NOT NULL DEFAULT 'CHAT',
		origin TEXT
	)`); err != nil {
		t.Fatalf("create chats: %v", err)
	}
	return db
}

// insert adds one row; an empty origin is stored as NULL, which is what an
// unstamped chat looks like on disk.
func insert(t *testing.T, db *sql.DB, id, agentID, wsID, mode, origin string) {
	t.Helper()
	var o any
	if origin != "" {
		o = origin
	}
	if _, err := db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, mode, origin) VALUES (?, ?, ?, ?, ?)`,
		id, agentID, wsID, mode, o); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// seedMatrix inserts one row per (mode, origin) combination under agent ag-1
// and returns the kind Of assigns to each, keyed by id.
func seedMatrix(t *testing.T, db *sql.DB, wsID string) map[string]Kind {
	t.Helper()
	want := map[string]Kind{}
	for _, mode := range testModes {
		for _, origin := range testOrigins {
			id := fmt.Sprintf("%s-%s", mode, originLabel(origin))
			insert(t, db, id, "ag-1", wsID, mode, origin)
			want[id] = Of(mode, origin)
		}
	}
	return want
}

func selectIDs(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func originLabel(origin string) string {
	if origin == "" {
		return "NULL"
	}
	return origin
}

func mustParse(t *testing.T, raw string) string {
	t.Helper()
	where, err := ParseFilter(raw)
	if err != nil {
		t.Fatalf("ParseFilter(%q): %v", raw, err)
	}
	return where
}
