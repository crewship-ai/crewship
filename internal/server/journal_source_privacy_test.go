package server

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestVerifiedTextJournalBridgeSuppressesWorkspaceProjection(t *testing.T) {
	db := testutil.MigratedDB(t)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('source-ws','Workspace','source-ws')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('source-agent','source-ws','Agent','source-agent')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('source-crew','source-ws','Crew','source-crew')`,
		`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,source_run_id,created_at) VALUES('private-source','source-ws','source-crew','private-chat','source-agent','private title','TEXT','verified-run','2026-10-16T12:00:00Z'),('legacy-source','source-ws','source-crew','legacy-chat','source-agent','legacy title','TEXT',NULL,'2026-10-16T12:00:00Z'),('credential-source','source-ws','source-crew','private-chat','source-agent','credential title','CREDENTIAL','verified-run','2026-10-16T12:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	broadcaster := newFakeBroadcaster()
	bridge := newJournalWSBridgeWithFilter(broadcaster, nil, func(entry journal.Entry) bool { return allowBroadSourceJournal(context.Background(), db.DB, entry) })
	entries := []journal.Entry{}
	for _, id := range []string{"private-source", "legacy-source", "credential-source"} {
		entries = append(entries, journal.Entry{ID: id, WorkspaceID: "source-ws", Type: journal.EntryPeerEscalation, Summary: id, Refs: map[string]any{"escalation_id": id}})
	}
	// A following legacy entry proves the drain passed the withheld private row.
	bridge.observe(entries)
	broadcaster.waitForCalls(t, 2)
	calls := broadcaster.snapshot()
	if len(calls) != 2 {
		t.Fatalf("unexpected broad frames: %+v", calls)
	}
	for i, expected := range []string{"legacy-source", "credential-source"} {
		payload := calls[i].payload.(map[string]any)
		if payload["id"] != expected {
			t.Fatalf("private source broad journal frame: %+v", payload)
		}
	}
}
