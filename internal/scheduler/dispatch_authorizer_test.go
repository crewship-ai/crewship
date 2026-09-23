package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestScheduledAuthorizer_RechecksAuthorityAfterAcceptance(t *testing.T) {
	tests := []struct {
		name    string
		change  string
		change2 string
		edit    func(*work.Item)
		want    dispatch.Decision
	}{
		{name: "unchanged", want: dispatch.Allow()},
		{name: "prompt edit keeps accepted input", change: `UPDATE agents SET schedule_prompt='new prompt', schedule_cron='*/2 * * * *' WHERE id='a1'`, want: dispatch.Allow()},
		{name: "disabled", change: `UPDATE agents SET schedule_enabled=0 WHERE id='a1'`, want: dispatch.Refuse("schedule was disabled while work waited")},
		{name: "cron removed", change: `UPDATE agents SET schedule_cron=NULL WHERE id='a1'`, want: dispatch.Refuse("schedule was disabled while work waited")},
		{name: "agent deleted", change: `UPDATE agents SET deleted_at='2026-09-22T12:08:00Z' WHERE id='a1'`, want: dispatch.Refuse("scheduled agent or workspace changed after acceptance")},
		{name: "workspace changed", change: `UPDATE agents SET workspace_id='ws2' WHERE id='a1'`, want: dispatch.Refuse("scheduled agent or workspace changed after acceptance")},
		{name: "held for review", change: `UPDATE agents SET status='PENDING_REVIEW' WHERE id='a1'`, want: dispatch.NotYet("scheduled agent awaits approval", 30*time.Second)},
		{name: "crew deleted", change: `UPDATE crews SET deleted_at='2026-09-22T12:08:00Z' WHERE id='crew1'`, want: dispatch.Refuse("scheduled agent crew changed after acceptance")},
		{name: "held agent with deleted crew", change: `UPDATE agents SET status='PENDING_REVIEW' WHERE id='a1'`, change2: `UPDATE crews SET deleted_at='2026-09-22T12:08:00Z' WHERE id='crew1'`, want: dispatch.Refuse("scheduled agent crew changed after acceptance")},
		{name: "crew changed", change: `UPDATE agents SET crew_id=NULL WHERE id='a1'`, want: dispatch.Refuse("scheduled agent changed crew after acceptance")},
		{name: "input changed", edit: func(item *work.Item) {
			item.InputJSON = `{"version":1,"agent_id":"a1","occurrence":"2026-09-22T12:00:00Z","prompt":"tampered"}`
		}, want: dispatch.Refuse("scheduled work input checksum changed")},
		{name: "wrong agent identity", edit: func(item *work.Item) { item.AgentID = "other" }, want: dispatch.Refuse("scheduled work has invalid immutable input")},
		{name: "wrong source", edit: func(item *work.Item) { item.Source = work.SourceWebhook }, want: dispatch.Refuse("work is not a scheduled agent run")},
		{name: "wrong class", edit: func(item *work.Item) { item.Class = work.ClassChat }, want: dispatch.Refuse("work is not a scheduled agent run")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, store := dueFixture(t)
			if tt.name == "workspace changed" {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES('ws2','Other','other')`); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := acceptDue(t.Context(), db, store, "ws1", work.IngressLimits{})
			if err != nil {
				t.Fatal(err)
			}
			item, err := store.Get(t.Context(), receipt.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if tt.change != "" {
				if _, err := db.ExecContext(t.Context(), tt.change); err != nil {
					t.Fatal(err)
				}
			}
			if tt.change2 != "" {
				if _, err := db.ExecContext(t.Context(), tt.change2); err != nil {
					t.Fatal(err)
				}
			}
			if tt.edit != nil {
				tt.edit(item)
			}
			got, err := NewScheduledAuthorizer(db).Authorize(t.Context(), dispatch.Assignment{Item: item})
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decision=%#v, err=%v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestScheduledAuthorizer_UnavailableDatabaseDoesNotAllow(t *testing.T) {
	db, store := dueFixture(t)
	receipt, err := acceptDue(t.Context(), db, store, "ws1", work.IngressLimits{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(t.Context(), receipt.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewScheduledAuthorizer(nil).Authorize(t.Context(), dispatch.Assignment{Item: item}); err == nil {
		t.Fatal("unavailable database must not authorize execution")
	}
}
