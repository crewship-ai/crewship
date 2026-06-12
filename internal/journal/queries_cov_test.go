package journal

// Coverage tests for queries.go — the filter branches in List and Count
// that the existing suites don't reach (multi-valued IN filters,
// mission/trace, exclude types, actor types, priorities, until bound,
// cursor pagination, Count's full filter mirror and FTS join).

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedFilterFixtures writes a small matrix of entries with distinct
// crew/agent/mission/trace/type/severity/actor/priority values so each
// filter test can assert an exact result set.
func seedFilterFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{ID: "f1", WorkspaceID: "ws_test", CrewID: "crew_a", AgentID: "ag_1", MissionID: "m_1",
			Type: EntryPeerConversation, Severity: SeverityInfo, ActorType: ActorAgent,
			Priority: PriorityNormal, TraceID: "tr_1", Summary: "alpha one", TS: base},
		{ID: "f2", WorkspaceID: "ws_test", CrewID: "crew_b", AgentID: "ag_2", MissionID: "m_2",
			Type: EntryPeerEscalation, Severity: SeverityWarn, ActorType: ActorSidecar,
			Priority: PriorityHigh, TraceID: "tr_2", Summary: "bravo two", TS: base.Add(time.Minute)},
		{ID: "f3", WorkspaceID: "ws_test", CrewID: "crew_c", AgentID: "ag_3",
			Type: EntryLLMCall, Severity: SeverityError, ActorType: ActorSystem,
			Priority: PriorityPermanent, Summary: "charlie three", TS: base.Add(2 * time.Minute)},
	}
	for _, e := range entries {
		if _, err := w.Emit(ctx, e); err != nil {
			t.Fatalf("emit %s: %v", e.ID, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func listIDs(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestList_FilterBranches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedFilterFixtures(t, db)
	ctx := context.Background()

	cases := []struct {
		name string
		q    Query
		want []string // newest-first order
	}{
		{"crew_ids_in", Query{WorkspaceID: "ws_test", CrewIDs: []string{"crew_a", "crew_b"}}, []string{"f2", "f1"}},
		{"single_crew", Query{WorkspaceID: "ws_test", CrewID: "crew_c"}, []string{"f3"}},
		{"agent_ids_in", Query{WorkspaceID: "ws_test", AgentIDs: []string{"ag_1", "ag_3"}}, []string{"f3", "f1"}},
		{"single_agent", Query{WorkspaceID: "ws_test", AgentID: "ag_2"}, []string{"f2"}},
		{"mission", Query{WorkspaceID: "ws_test", MissionID: "m_1"}, []string{"f1"}},
		{"trace", Query{WorkspaceID: "ws_test", TraceID: "tr_2"}, []string{"f2"}},
		{"types", Query{WorkspaceID: "ws_test", Types: []EntryType{EntryLLMCall}}, []string{"f3"}},
		{"exclude_types", Query{WorkspaceID: "ws_test", ExcludeTypes: []EntryType{EntryLLMCall, EntryPeerEscalation}}, []string{"f1"}},
		{"severities", Query{WorkspaceID: "ws_test", Severities: []Severity{SeverityWarn, SeverityError}}, []string{"f3", "f2"}},
		{"actor_types", Query{WorkspaceID: "ws_test", ActorTypes: []ActorType{ActorSidecar}}, []string{"f2"}},
		{"priorities", Query{WorkspaceID: "ws_test", Priorities: []Priority{PriorityHigh, PriorityPermanent}}, []string{"f3", "f2"}},
		{"since", Query{WorkspaceID: "ws_test", Since: time.Date(2026, 6, 1, 12, 1, 30, 0, time.UTC)}, []string{"f3"}},
		{"until", Query{WorkspaceID: "ws_test", Until: time.Date(2026, 6, 1, 12, 0, 30, 0, time.UTC)}, []string{"f1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := List(ctx, db, c.q)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			ids := listIDs(got)
			if len(ids) != len(c.want) {
				t.Fatalf("got %v, want %v", ids, c.want)
			}
			for i := range ids {
				if ids[i] != c.want[i] {
					t.Fatalf("got %v, want %v", ids, c.want)
				}
			}
		})
	}
}

func TestList_CursorWalksAllPages(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedFilterFixtures(t, db)
	ctx := context.Background()

	var (
		cursor string
		seen   []string
	)
	for page := 0; page < 5; page++ {
		entries, next, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(entries) == 0 {
			break
		}
		seen = append(seen, entries[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	want := []string{"f3", "f2", "f1"}
	if len(seen) != len(want) {
		t.Fatalf("cursor walk saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("cursor walk order %v, want %v", seen, want)
		}
	}
}

func TestCount_MirrorsListFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedFilterFixtures(t, db)
	ctx := context.Background()

	cases := []struct {
		name string
		q    Query
		want int64
	}{
		{"all", Query{WorkspaceID: "ws_test"}, 3},
		{"crew_ids_in", Query{WorkspaceID: "ws_test", CrewIDs: []string{"crew_a", "crew_b"}}, 2},
		{"single_crew", Query{WorkspaceID: "ws_test", CrewID: "crew_a"}, 1},
		{"agent_ids_in", Query{WorkspaceID: "ws_test", AgentIDs: []string{"ag_1"}}, 1},
		{"single_agent", Query{WorkspaceID: "ws_test", AgentID: "ag_3"}, 1},
		{"mission", Query{WorkspaceID: "ws_test", MissionID: "m_2"}, 1},
		{"trace", Query{WorkspaceID: "ws_test", TraceID: "tr_1"}, 1},
		{"types", Query{WorkspaceID: "ws_test", Types: []EntryType{EntryPeerConversation, EntryLLMCall}}, 2},
		{"exclude_types", Query{WorkspaceID: "ws_test", ExcludeTypes: []EntryType{EntryLLMCall}}, 2},
		{"severities", Query{WorkspaceID: "ws_test", Severities: []Severity{SeverityError}}, 1},
		{"actor_types", Query{WorkspaceID: "ws_test", ActorTypes: []ActorType{ActorAgent, ActorSystem}}, 2},
		{"priorities", Query{WorkspaceID: "ws_test", Priorities: []Priority{PriorityNormal}}, 1},
		{"since", Query{WorkspaceID: "ws_test", Since: time.Date(2026, 6, 1, 12, 0, 30, 0, time.UTC)}, 2},
		{"until", Query{WorkspaceID: "ws_test", Until: time.Date(2026, 6, 1, 12, 1, 30, 0, time.UTC)}, 2},
		{"no_match", Query{WorkspaceID: "ws_other"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Count(ctx, db, c.q)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if got != c.want {
				t.Errorf("Count = %d, want %d", got, c.want)
			}
			// Count must agree with the size of the List result set.
			entries, _, err := List(ctx, db, c.q)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if int64(len(entries)) != got {
				t.Errorf("Count %d disagrees with List %d (%v)", got, len(entries), listIDs(entries))
			}
		})
	}
}

func TestCount_FTSJoinMatchesList(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	seedFilterFixtures(t, db)
	ctx := context.Background()

	q := Query{WorkspaceID: "ws_test", FTSQuery: "bravo"}
	n, err := Count(ctx, db, q)
	if err != nil {
		t.Fatalf("Count(FTS): %v", err)
	}
	if n != 1 {
		t.Errorf("FTS count = %d, want 1", n)
	}
	entries, _, err := List(ctx, db, q)
	if err != nil {
		t.Fatalf("List(FTS): %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "f2" {
		t.Errorf("FTS list = %v, want [f2]", listIDs(entries))
	}
}

func TestListAndCount_ClosedDBErrors(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	ctx := context.Background()

	if _, _, err := List(ctx, db, Query{WorkspaceID: "ws_test"}); err == nil {
		t.Error("List on closed DB should error")
	}
	if _, err := Count(ctx, db, Query{WorkspaceID: "ws_test"}); err == nil {
		t.Error("Count on closed DB should error")
	}
	if _, err := Get(ctx, db, "ws_test", "x"); err == nil {
		t.Error("Get on closed DB should error")
	}
}

func TestList_HydratesExpiresAtAndRefs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	exp := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if _, err := w.Emit(ctx, Entry{
		ID: "x1", WorkspaceID: "ws_test", Type: EntryPeerConversation,
		ActorType: ActorAgent, Summary: "expiring",
		Payload:   map[string]any{"k": "v"},
		Refs:      map[string]any{"mission": "m_9"},
		ExpiresAt: &exp,
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := List(ctx, db, Query{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	e := entries[0]
	if e.ExpiresAt == nil || !e.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", e.ExpiresAt, exp)
	}
	if e.Payload["k"] != "v" {
		t.Errorf("payload not hydrated: %v", e.Payload)
	}
	if e.Refs["mission"] != "m_9" {
		t.Errorf("refs not hydrated: %v", e.Refs)
	}
}
