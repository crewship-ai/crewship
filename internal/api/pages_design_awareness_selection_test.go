package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func designTestAwareness(id uint64, state string) []byte {
	payload := designAppendUint(nil, 1)
	payload = designAppendUint(payload, id)
	payload = designAppendUint(payload, 1)
	payload = designAppendUint(payload, uint64(len(state)))
	payload = append(payload, state...)
	raw := designAppendUint(nil, 1)
	raw = designAppendUint(raw, uint64(len(payload)))
	return append(raw, payload...)
}

// designTestAwarenessState decodes the JSON state of a one-client awareness frame.
func designTestAwarenessState(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	i := 0
	for _, step := range []string{"kind", "length", "count", "id", "clock", "state length"} {
		if _, err := designUint(frame, &i); err != nil {
			t.Fatalf("decode %s: %v", step, err)
		}
	}
	var state map[string]any
	if err := json.Unmarshal(frame[i:], &state); err != nil {
		t.Fatalf("decode state %q: %v", frame[i:], err)
	}
	return state
}

func TestDesignAwarenessSelectionReachesOnlyLiveReaders(t *testing.T) {
	editor := &designClient{actor: "editor", displayName: "Editor", canEdit: true, canReadLive: true, out: make(chan []byte, 4)}
	live := &designClient{actor: "manager", canReadLive: true, out: make(chan []byte, 4)}
	shared := &designClient{actor: "reader", out: make(chan []byte, 4)}
	room := &designRoom{clients: map[*designClient]bool{editor: true, live: true, shared: true}}

	if _, err := designAwareness(designTestAwareness(7, `{"cursor":{"x":1,"y":2},"selection":"cta","user":{"name":"spoof"}}`), editor, room); err != nil {
		t.Fatal(err)
	}
	room.broadcastAwareness(editor)
	got := designTestAwarenessState(t, <-live.out)
	if got["selection"] != "cta" {
		t.Fatalf("a live reader must see the selection, got %v", got)
	}
	hidden := designTestAwarenessState(t, <-shared.out)
	if _, ok := hidden["selection"]; ok {
		t.Fatalf("a shared-only reader must not receive the live selection, got %v", hidden)
	}
	if hidden["cursor"] == nil || hidden["user"].(map[string]any)["name"] != "Editor" {
		t.Fatalf("a shared-only reader keeps presence and cursor, got %v", hidden)
	}
	// A newcomer gets what it may see too.
	if state := designTestAwarenessState(t, editor.awarenessFor(shared)); state["selection"] != nil {
		t.Fatalf("cached awareness for a shared-only reader holds the selection: %v", state)
	}

	// A shared-only reader cannot send a selection; null and bounded IDs pass for editors only.
	if _, err := designAwareness(designTestAwareness(8, `{"selection":"cta"}`), shared, room); err != nil {
		t.Fatal(err)
	}
	if _, ok := designTestAwarenessState(t, shared.awareness)["selection"]; ok {
		t.Fatal("a shared-only reader's selection must be dropped")
	}
	for state, keep := range map[string]bool{
		`{"selection":null}`: true,
		`{"selection":"` + strings.Repeat("a", designMaxSelection) + `"}`:   true,
		`{"selection":"` + strings.Repeat("a", designMaxSelection+1) + `"}`: false,
		`{"selection":""}`:             false,
		`{"selection":{"id":"cta"}}`:   false,
		`{"selection":["cta","copy"]}`: true,
		`{"selection":[]}`:             false,
		`{"selection":["cta",""]}`:     false,
		`{"selection":["cta",7]}`:      false,
		`{"selection":["` + strings.Repeat("a", designMaxSelection+1) + `"]}`:                          false,
		`{"selection":[` + strings.TrimSuffix(strings.Repeat(`"a",`, designMaxSelected), ",") + `]}`:   true,
		`{"selection":[` + strings.TrimSuffix(strings.Repeat(`"a",`, designMaxSelected+1), ",") + `]}`: false,
	} {
		if _, err := designAwareness(designTestAwareness(7, state), editor, room); err != nil {
			t.Fatal(err)
		}
		if _, ok := designTestAwarenessState(t, editor.awareness)["selection"]; ok != keep {
			t.Fatalf("%s: selection kept=%v, want %v", state, ok, keep)
		}
	}
}

// A multi-selection (#5217) is live-canvas state like a single one: a live
// reader sees every selected ID, a shared-only reader none of them.
func TestDesignAwarenessMultiSelectionReachesOnlyLiveReaders(t *testing.T) {
	editor := &designClient{actor: "editor", displayName: "Editor", canEdit: true, canReadLive: true, out: make(chan []byte, 4)}
	live := &designClient{actor: "manager", canReadLive: true, out: make(chan []byte, 4)}
	shared := &designClient{actor: "reader", out: make(chan []byte, 4)}
	room := &designRoom{clients: map[*designClient]bool{editor: true, live: true, shared: true}}
	if _, err := designAwareness(designTestAwareness(7, `{"selection":["cta","copy"]}`), editor, room); err != nil {
		t.Fatal(err)
	}
	room.broadcastAwareness(editor)
	got, _ := json.Marshal(designTestAwarenessState(t, <-live.out)["selection"])
	if string(got) != `["cta","copy"]` {
		t.Fatalf("a live reader must see the whole selection, got %s", got)
	}
	if _, ok := designTestAwarenessState(t, <-shared.out)["selection"]; ok {
		t.Fatal("a shared-only reader must not receive a multi-selection")
	}
	if _, err := designAwareness(designTestAwareness(8, `{"selection":["cta","copy"]}`), shared, room); err != nil {
		t.Fatal(err)
	}
	if _, ok := designTestAwarenessState(t, shared.awareness)["selection"]; ok {
		t.Fatal("a shared-only reader's multi-selection must be dropped")
	}
}
