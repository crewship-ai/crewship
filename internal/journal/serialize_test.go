package journal

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSerializeEntryKeepsCapabilityAuditSetsPrivate(t *testing.T) {
	payload := map[string]any{
		"subject_user_id": "member", "action": "capabilities_changed",
		"previous_capabilities": []string{"chat"},
		"capabilities":          []string{"chat", "credentials:reveal"},
	}
	want := map[string]any{"subject_user_id": "member", "action": "capabilities_changed"}
	for _, entry := range SerializeEntries([]Entry{{Type: EntryMemberCapabilitiesChanged, Payload: payload}}) {
		if !reflect.DeepEqual(entry["payload"], want) {
			t.Fatalf("capability audit exposed through shared feed: %v", entry)
		}
	}
	if len(payload) != 4 || !reflect.DeepEqual(payload["capabilities"], []string{"chat", "credentials:reveal"}) || !reflect.DeepEqual(payload["previous_capabilities"], []string{"chat"}) {
		t.Fatal("serialization changed retained audit state")
	}
}

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

func TestSerializeEntryKeepsHostAuthorityOutOfPublicFeeds(t *testing.T) {
	payload := map[string]any{
		"human_authority":    map[string]any{"user_id": "private-user"},
		"trigger_message_id": "private-message", "chat_id": "private-chat",
		"host_admitted": true, "trigger_type": "USER",
	}
	e := Entry{Type: EntryRunStarted, Payload: payload}
	wire, err := json.Marshal(SerializeEntry(e))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(wire, &parsed); err != nil {
		t.Fatal(err)
	}
	public := parsed["payload"].(map[string]any)
	if len(public) != 1 || public["trigger_type"] != "USER" {
		t.Fatalf("private admission leaked: %s", wire)
	}
	if len(payload) != 5 || payload["human_authority"] == nil {
		t.Fatal("serialization altered persisted host authority")
	}
	privateOnly := Entry{Payload: map[string]any{"human_authority": payload["human_authority"], "chat_id": "private-chat"}}
	if _, exists := SerializeEntry(privateOnly)["payload"]; exists {
		t.Fatal("empty public payload must be omitted")
	}
}

func TestSerializeEntryKeepsDelegatedOriginPrivate(t *testing.T) {
	payload := map[string]any{
		"chat_origin_id": "private-origin", "chat_id": "private-root-chat",
		"assigned_by_chat": "private-dispatch-chat", "trigger_type": "ASSIGNMENT",
	}
	wire, err := json.Marshal(SerializeEntry(Entry{Type: EntryRunStarted, Payload: payload}))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(wire, &result); err != nil {
		t.Fatal(err)
	}
	public := result["payload"].(map[string]any)
	if len(public) != 1 || public["trigger_type"] != "ASSIGNMENT" {
		t.Fatalf("delegated origin leaked: %s", wire)
	}
	if len(payload) != 4 || payload["chat_origin_id"] != "private-origin" {
		t.Fatal("serialization altered durable delegated authority")
	}
}

func TestSerializeMemberAddedKeepsInitialAuthorityPrivate(t *testing.T) {
	payload := map[string]any{"member_id": "member", "subject_user_id": "subject", "action": "added", "role": "VIEWER", "access_mode": "restricted", "rights": []any{map[string]any{"id": "private-resource"}}, "email": "private@example.test", "setup_url": "private-setup"}
	original, _ := json.Marshal(payload)
	wire := SerializeEntry(Entry{Type: EntryMemberAdded, ActorType: ActorUser, ActorID: "actor", Payload: payload})
	expected := map[string]any{"member_id": "member", "subject_user_id": "subject", "action": "added"}
	if !reflect.DeepEqual(wire["payload"], expected) || wire["actor_id"] != "actor" {
		t.Fatalf("public membership projection expanded: %#v", wire)
	}
	after, _ := json.Marshal(payload)
	if string(original) != string(after) {
		t.Fatal("public projection changed canonical chain payload")
	}
}
