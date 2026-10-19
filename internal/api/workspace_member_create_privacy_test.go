package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type memberPrivacyEnqueuer struct{ calls []pipeline.PendingRun }

func (e *memberPrivacyEnqueuer) Enqueue(_ context.Context, p pipeline.PendingRun) (string, bool, error) {
	e.calls = append(e.calls, p)
	return p.ID, false, nil
}

// Exercise the actual HTTP producer and its native canonical entry, not a
// fabricated feed DTO. No issued setup secret is used or emitted by this test.
func TestMemberAddedPrivateSnapshotConsumers(t *testing.T) {
	f, _ := memberCreateFixture(t, "new")
	execOrFatal(t, f.db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('privacyprojectneedle',?,'Fixture','privacyprojectneedle')`, f.workspace)
	out := f.do("POST", "/members/provision", `{"email":"member-create@example.test","role":"VIEWER","access_mode":"restricted","rights":[{"kind":"project","id":"privacyprojectneedle","operation":"list"}]}`)
	if out.Code != 201 {
		t.Fatalf("native producer calibration: status=%d", out.Code)
	}
	entries, _, err := journal.List(t.Context(), f.db, journal.Query{WorkspaceID: f.workspace, Types: []journal.EntryType{journal.EntryMemberAdded}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("native event calibration: count=%d err=%v", len(entries), err)
	}
	e := entries[0]
	var before string
	if err := f.db.QueryRow(`SELECT payload||'|'||entry_hash FROM journal_entries WHERE id=?`, e.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if e.Payload["role"] != "VIEWER" || e.Payload["access_mode"] != "restricted" || !strings.Contains(before, "privacyprojectneedle") {
		t.Fatal("canonical stored authority missing")
	}
	f.checkChain(t)
	defer func() {
		var after string
		if err := f.db.QueryRow(`SELECT payload||'|'||entry_hash FROM journal_entries WHERE id=?`, e.ID).Scan(&after); err != nil || after != before {
			t.Error("consumer changed canonical snapshot/hash")
		}
		f.checkChain(t)
	}()
	private := automation.Matcher{PayloadEquals: map[string]any{"role": "VIEWER"}}
	if !private.Matches(e) {
		t.Fatal("raw matcher positive calibration failed")
	}
	t.Run("preview", func(t *testing.T) {
		public := automation.Preview(automation.Matcher{}, string(e.Type), entries)
		if len(public.Samples) != 1 {
			t.Fatal("public event calibration missing")
		}
		wire, _ := json.Marshal(public.Samples)
		if strings.Contains(string(wire), "privacyprojectneedle") || public.Samples[0].Payload["role"] != nil {
			t.Error("member.added Preview sample exposes canonical-only authority")
		}
		denied := automation.Preview(private, string(e.Type), entries)
		if denied.Matched != 0 || !denied.TopRejection.KeyAbsent {
			t.Error("member.added Preview private matcher oracle")
		}
	})
	t.Run("observer", func(t *testing.T) {
		enq := &memberPrivacyEnqueuer{}
		r := automation.NewRegistry(nil, enq, automation.Options{})
		mk := func(id string, m automation.Matcher) automation.Resolved {
			return automation.Resolved{Automation: automation.Automation{ID: id, WorkspaceID: f.workspace, Name: id, Enabled: true, EventType: string(e.Type), Matcher: m, ActionKind: automation.ActionKindRoutine, Action: automation.Action{RoutineSlug: "fixture", Inputs: map[string]any{"private": "{{ event.payload.role }}", "public": "{{ event.payload.action }}"}}, DebounceSeconds: 1, MaxPerHour: 60}, PipelineID: "fixture", PipelineSlug: "fixture"}
		}
		r.Load([]automation.Resolved{mk("private", private), mk("public", automation.Matcher{})})
		r.Observer(entries)
		r.Flush(t.Context())
		if len(enq.calls) != 1 {
			t.Errorf("member.added observer private matcher fired/public event lost: %d", len(enq.calls))
		}
		for _, p := range enq.calls {
			if strings.Contains(p.InputsJSON, "VIEWER") || !strings.Contains(p.InputsJSON, "added") {
				t.Error("member.added observer renders private authority/public action lost")
			}
		}
	})
	t.Run("fts", func(t *testing.T) {
		for _, tc := range []struct {
			query string
			want  int
		}{{"Workspace member added", 1}, {"privacyprojectneedle", 0}} {
			q := journal.Query{WorkspaceID: f.workspace, Types: []journal.EntryType{e.Type}, FTSQuery: tc.query}
			rows, _, err := journal.List(t.Context(), f.db, q)
			n, ce := journal.Count(t.Context(), f.db, q)
			if err != nil || ce != nil {
				t.Fatalf("native FTS setup: %v %v", err, ce)
			}
			if len(rows) != tc.want || n != int64(tc.want) {
				t.Errorf("member.added FTS query %q reveals canonical snapshot: List=%d Count=%d want=%d", tc.query, len(rows), n, tc.want)
			}
		}
	})
}
