package journal

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSerializeEntriesWireContract(t *testing.T) {
	stamp := time.Date(2026, 10, 2, 9, 30, 0, 123456789, time.FixedZone("offset", 2*60*60))
	minimal := Entry{ID: "first", WorkspaceID: "ws", TS: stamp, Type: EntryRunStarted, Severity: SeverityInfo, Priority: PriorityNormal, ActorType: ActorAgent, Summary: "started"}
	full := minimal
	full.ID = "second"
	full.CrewID, full.AgentID, full.MissionID = "crew", "agent", "mission"
	full.ActorID, full.TraceID = "actor", "trace"
	full.Payload = map[string]any{"run_id": "run"}
	full.Refs = map[string]any{"issue_id": "issue"}
	rows := SerializeEntries([]Entry{minimal, full})
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"id": "first", "workspace_id": "ws", "ts": "2026-10-02T07:30:00.123456789Z", "entry_type": "run.started", "severity": "info", "priority": "normal", "actor_type": "agent", "summary": "started"},
		{"id": "second", "workspace_id": "ws", "ts": "2026-10-02T07:30:00.123456789Z", "entry_type": "run.started", "severity": "info", "priority": "normal", "actor_type": "agent", "summary": "started", "crew_id": "crew", "agent_id": "agent", "mission_id": "mission", "actor_id": "actor", "trace_id": "trace", "payload": map[string]any{"run_id": "run"}, "refs": map[string]any{"issue_id": "issue"}},
	}
	if !reflect.DeepEqual(wire, want) {
		t.Fatalf("wire shape changed: got %s", encoded)
	}
	minimal.Payload, minimal.Refs = map[string]any{}, map[string]any{}
	if !reflect.DeepEqual(SerializeEntry(minimal), rows[0]) {
		t.Fatal("empty optional maps must be omitted from live feed frames")
	}
}

func TestSerializeEntriesEmptyArray(t *testing.T) {
	for _, entries := range [][]Entry{nil, {}} {
		encoded, err := json.Marshal(SerializeEntries(entries))
		if err != nil || string(encoded) != "[]" {
			t.Fatalf("empty feed must be a JSON array: %s err=%v", encoded, err)
		}
	}
}
