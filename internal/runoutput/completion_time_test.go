//go:build unix

package runoutput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func completionField(t *testing.T, result *Result) string {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		CompletedAt string `json:"completed_at"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value.CompletedAt
}

func TestCapturedCompletionTimeSurvivesReplay(t *testing.T) {
	dir, store := replayFixture(t, false)
	before := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	snapshot, err := Inspect(t.Context(), filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	captured := completionField(t, snapshot.Result)
	finished, err := time.Parse(time.RFC3339Nano, captured)
	if err != nil || finished.Before(before) || finished.After(after) {
		t.Fatalf("protected exit lost its captured completion time: %q (%v)", captured, err)
	}
	crash := errors.New("projection ACK lost")
	calls := 0
	complete := func(value Snapshot) error {
		calls++
		if got := completionField(t, value.Result); got != captured {
			t.Fatalf("replay changed completion: got %q want %q", got, captured)
		}
		if calls == 1 {
			return crash
		}
		return nil
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, complete); !errors.Is(err, crash) {
		t.Fatalf("first projection: %v", err)
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, complete); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, complete); err != nil || calls != 2 {
		t.Fatalf("acknowledged result replayed: calls=%d err=%v", calls, err)
	}
}

func TestLegacyCompletionTimeRemainsUnknownAndBytesStable(t *testing.T) {
	dir, store := replayFixture(t, false)
	// Exact pre-completion-time record: do not invent a finish from replay time.
	if err := store.appendLocked(Record{Kind: "exit", ExitCode: 0, Reason: "exited"}, true); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "output", "events.jsonl")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(t.Context(), filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot.Result)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"exit_code":0,"reason":"exited"}` {
		t.Fatalf("legacy result acquired manufactured time or new bytes: %s", raw)
	}
	for i := 0; i < 2; i++ {
		if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(value Snapshot) error {
			if completionField(t, value.Result) != "" {
				t.Fatal("legacy finish was invented")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacyHash := sha256.Sum256(before)
	if ack.Hash != hex.EncodeToString(legacyHash[:]) {
		t.Fatal("legacy prefix fingerprint changed under new reader")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Fatalf("legacy capture rewritten: %v", err)
	}
}

func TestCapturedCompletionUsesExactDurableEventTime(t *testing.T) {
	dir, store := replayFixture(t, false)
	completed := time.Date(2026, 10, 19, 10, 0, 42, 125000000, time.UTC)
	if err := store.FinishAt(0, "exited", completed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(t.Context(), filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	if got := completionField(t, snapshot.Result); got != "2026-10-19T10:00:42.125000000Z" {
		t.Fatalf("captured T1 changed: %q", got)
	}
	decoder, err := NewLineDecoder(1024, func(Line) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCommitted(filepath.Join(dir, "output"), 0, decoder.Push); err != nil || !decoder.Complete() {
		t.Fatalf("line decoder rejected captured terminal: %v", err)
	}
}

func TestMalformedCapturedCompletionIsRejected(t *testing.T) {
	for _, value := range []string{"unknown", "0001-01-01T00:00:00Z"} {
		raw := []byte(`{"version":1,"sequence":2,"complete":true,"result":{"exit_code":0,"reason":"exited","completed_at":"` + value + `"}}`)
		var snapshot Snapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Validate() == nil {
			t.Fatalf("unvalidated completion propagated: %q", value)
		}
	}
}
