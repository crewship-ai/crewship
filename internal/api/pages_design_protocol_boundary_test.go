package api

import "testing"

func TestDesignUintUsesJavaScriptSafeIntegerBoundary(t *testing.T) {
	const maximum uint64 = 1<<53 - 1
	for _, value := range []uint64{0, 127, 128, maximum} {
		raw := designAppendUint(nil, value)
		index := 0
		got, err := designUint(raw, &index)
		if err != nil || got != value || index != len(raw) {
			t.Fatalf("valid standard integer %d: got %d index %d error %v", value, got, index, err)
		}
	}
	for _, value := range []uint64{1 << 53, 1<<56 - 1} {
		raw := designAppendUint(nil, value)
		index := 0
		if got, err := designUint(raw, &index); err == nil {
			t.Fatalf("unsafe integer %d accepted as %d", value, got)
		}
	}
}

func TestDesignAwarenessRejectsUnsafeClientIDAndClockBeforeCaching(t *testing.T) {
	const maximum uint64 = 1<<53 - 1
	frame := func(id, clock uint64) []byte {
		payload := designAppendUint(nil, 1)
		payload = designAppendUint(payload, id)
		payload = designAppendUint(payload, clock)
		payload = designAppendUint(payload, 2)
		payload = append(payload, []byte("{}")...)
		raw := designAppendUint(nil, 1)
		raw = designAppendUint(raw, uint64(len(payload)))
		return append(raw, payload...)
	}
	room := &designRoom{clients: map[*designClient]bool{}}
	for _, tc := range []struct {
		name      string
		id, clock uint64
	}{
		{"client ID", 1 << 53, 1}, {"clock", 1, 1 << 53},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &designClient{}
			if _, err := designAwareness(frame(tc.id, tc.clock), client, room); err == nil {
				t.Fatal("unsafe awareness integer accepted")
			}
			if client.hasID || len(client.awareness) != 0 {
				t.Fatal("rejected awareness was cached for broadcast")
			}
		})
	}
	client := &designClient{}
	if _, err := designAwareness(frame(maximum, maximum), client, room); err != nil {
		t.Fatalf("maximum safe integer rejected: %v", err)
	}
	if client.clientID != maximum || client.clock != maximum {
		t.Fatal("maximum safe awareness value changed")
	}
	// Disconnecting the maximum valid clock must not manufacture an unsafe
	// value. Standard y-protocols accepts removal at the same clock with null.
	gone := designAwarenessGone(client)
	removal := &designClient{}
	if _, err := designAwareness(gone, removal, room); err != nil {
		t.Fatalf("disconnect emitted invalid awareness: %v", err)
	}
	if removal.clock != maximum {
		t.Fatalf("disconnect overflowed safe clock: %d", removal.clock)
	}
}
