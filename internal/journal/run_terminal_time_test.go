package journal

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCapturedTerminalTimeRetainsFingerprintAfterPruning(t *testing.T) {
	started := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	const captured = "2026-10-19T10:00:42.125000000Z"
	completed, err := RunTerminalEventTime(started, captured)
	if err != nil || completed.Sub(started) != 42125*time.Millisecond {
		t.Fatalf("completion/duration: %s %v", completed, err)
	}
	db := openTestDB(t)
	defer db.Close()
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	var deliveries atomic.Int32
	writer.SetCommitObserver(func(entries []Entry) { deliveries.Add(int32(len(entries))) })
	code := 0
	entry := RunTerminalReplay("completion-run", "ws_test", "agent", completed, EntryRunCompleted, false, &code, nil, map[string]any{"captured_completed_at": captured})
	if _, fresh, err := writer.EmitReplaySync(t.Context(), entry); err != nil || !fresh {
		t.Fatalf("first terminal: fresh=%v %v", fresh, err)
	}
	var ts, fingerprint string
	if err := db.QueryRow(`SELECT ts FROM journal_entries WHERE id=?`, entry.ID).Scan(&ts); err != nil || ts != "2026-10-19T10:00:42.125Z" {
		t.Fatalf("terminal timestamp=%q: %v", ts, err)
	}
	if err := db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, entry.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := writer.EmitReplaySync(t.Context(), entry); err != nil || fresh {
		t.Fatalf("pruned terminal replay: fresh=%v %v", fresh, err)
	}
	var after string
	if err := db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, entry.ID).Scan(&after); err != nil || after != fingerprint || deliveries.Load() != 1 {
		t.Fatalf("replay changed receipt or observer delivery: deliveries=%d %v", deliveries.Load(), err)
	}
	changed := entry
	changed.TS = completed.Add(time.Second)
	if _, _, err := writer.EmitReplaySync(t.Context(), changed); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("replay clock changed immutable terminal: %v", err)
	}
}

func TestLegacyTerminalTimeRemainsDeterministicAndMalformedCaptureFails(t *testing.T) {
	started := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	got, err := RunTerminalEventTime(started, "")
	if err != nil || !got.Equal(started) {
		t.Fatalf("legacy fallback changed: %s %v", got, err)
	}
	for _, invalid := range []string{"unknown", "0001-01-01T00:00:00Z"} {
		if _, err := RunTerminalEventTime(started, invalid); err == nil {
			t.Fatalf("invalid captured time accepted: %q", invalid)
		}
	}
}
