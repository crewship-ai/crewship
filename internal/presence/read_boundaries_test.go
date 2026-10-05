package presence

import (
	"testing"
	"time"
)

func TestWorkspaceRosterIsOrderedScopedAndCarriesDetails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC()
	for _, snapshot := range []Snapshot{
		{AgentID: "older", WorkspaceID: "ws_test", CrewID: "crew", Status: StatusBusy, Since: now.Add(-time.Minute), Details: map[string]any{"task": "task-1"}},
		{AgentID: "newer", WorkspaceID: "ws_test", Status: StatusOnline, Since: now},
		{AgentID: "foreign", WorkspaceID: "ws_other", CrewID: "crew", Status: StatusOnline, Since: now.Add(time.Minute)},
	} {
		if err := Upsert(t.Context(), db, nil, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ListByWorkspace(t.Context(), db, "ws_test")
	if err != nil || len(rows) != 2 {
		t.Fatalf("workspace roster: %+v, %v", rows, err)
	}
	if rows[0].AgentID != "newer" || rows[1].AgentID != "older" || rows[1].Details["task"] != "task-1" {
		t.Fatalf("wrong roster ordering or metadata: %+v", rows)
	}
	if got, err := Get(t.Context(), db, "missing"); err != nil || got != nil {
		t.Fatalf("absent agent: %+v, %v", got, err)
	}
	if err := Upsert(t.Context(), db, nil, Snapshot{Status: StatusOnline}); err == nil {
		t.Fatal("missing identity accepted")
	}
}

func TestUnavailablePresenceStorageReturnsErrors(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := Get(t.Context(), db, "agent"); err == nil || got != nil {
		t.Fatalf("closed get: %+v, %v", got, err)
	}
	if got, err := ListByCrew(t.Context(), db, "crew"); err == nil || got != nil {
		t.Fatalf("closed crew: %+v, %v", got, err)
	}
	if got, err := ListByWorkspace(t.Context(), db, "ws"); err == nil || got != nil {
		t.Fatalf("closed workspace: %+v, %v", got, err)
	}
	if err := SweepOffline(t.Context(), db, nil, 0); err == nil {
		t.Fatal("closed sweep succeeded")
	}
	recorder := &recordingEmitter{}
	if err := Upsert(t.Context(), db, recorder, Snapshot{AgentID: "agent", WorkspaceID: "ws", Status: StatusOnline}); err == nil {
		t.Fatal("closed upsert succeeded")
	}
	if len(recorder.entries) != 0 {
		t.Fatal("announced an uncommitted status")
	}
}
