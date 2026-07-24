package server

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// fakeBroadcaster captures BroadcastChannel calls so a test can assert exactly
// where (and whether) the bridge fans out, without a live WebSocket.
type fakeBroadcaster struct {
	mu    sync.Mutex
	calls []broadcastCall
	got   chan struct{} // signalled after each recorded call
}

type broadcastCall struct {
	prefix    string
	id        string
	eventType string
	payload   any
}

func newFakeBroadcaster() *fakeBroadcaster {
	return &fakeBroadcaster{got: make(chan struct{}, 1024)}
}

func (f *fakeBroadcaster) BroadcastChannel(prefix, id, eventType string, payload any) {
	f.mu.Lock()
	f.calls = append(f.calls, broadcastCall{prefix, id, eventType, payload})
	f.mu.Unlock()
	select {
	case f.got <- struct{}{}:
	default:
	}
}

func (f *fakeBroadcaster) snapshot() []broadcastCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]broadcastCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitForCalls blocks until at least n calls have been recorded or the timeout
// elapses, so the async drain goroutine is given time to run.
func (f *fakeBroadcaster) waitForCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		f.mu.Lock()
		have := len(f.calls)
		f.mu.Unlock()
		if have >= n {
			return
		}
		select {
		case <-f.got:
		case <-deadline:
			t.Fatalf("timed out waiting for %d broadcast(s); got %d", n, have)
		}
	}
}

// TestJournalWSBridge_ObserveNeverBlocks is the load-bearing guarantee: the
// commit observer runs on the journal WRITE path, so observe() must return
// promptly even when flooded far past the buffer — it drops under
// backpressure rather than blocking a journal commit. A nil hub is safe
// (BroadcastChannel no-ops), so run() drains without a real hub.
func TestJournalWSBridge_ObserveNeverBlocks(t *testing.T) {
	b := newJournalWSBridge(nil, nil)

	entries := make([]journal.Entry, journalWSBridgeBuffer*3)
	for i := range entries {
		entries[i] = journal.Entry{WorkspaceID: "ws1", ID: strconv.Itoa(i), Type: journal.EntryRunStarted}
	}

	done := make(chan struct{})
	go func() {
		b.observe(entries)
		// Empty-workspace entries are skipped (no channel to route to) and
		// must not panic.
		b.observe([]journal.Entry{{WorkspaceID: ""}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("observe blocked under backpressure — it must be non-blocking")
	}
}

// TestJournalWSBridge_RoutesToOptInJournalChannel pins the firehose fix: the
// bridge fans out on the dedicated, opt-in `journal:{workspaceId}` channel as a
// `journal.entry` event — NOT the `workspace:{id}` channel every tab
// auto-subscribes to.
func TestJournalWSBridge_RoutesToOptInJournalChannel(t *testing.T) {
	fake := newFakeBroadcaster()
	b := newJournalWSBridgeWith(fake, slog.Default())

	b.observe([]journal.Entry{{WorkspaceID: "ws1", ID: "e1", Type: journal.EntryRunStarted}})
	fake.waitForCalls(t, 1)

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d broadcasts, want 1", len(calls))
	}
	c := calls[0]
	if c.prefix != "journal" {
		t.Errorf("prefix = %q, want journal (must NOT be the workspace firehose)", c.prefix)
	}
	if c.id != "ws1" {
		t.Errorf("id = %q, want ws1", c.id)
	}
	if c.eventType != "journal.entry" {
		t.Errorf("eventType = %q, want journal.entry", c.eventType)
	}
}

// TestJournalWSBridge_FiltersTelemetryAndUnscoped verifies observe() drops
// high-frequency telemetry and workspace-less entries before fan-out, so
// neither ever reaches the wire.
func TestJournalWSBridge_FiltersTelemetryAndUnscoped(t *testing.T) {
	fake := newFakeBroadcaster()
	b := newJournalWSBridgeWith(fake, slog.Default())

	b.observe([]journal.Entry{
		{WorkspaceID: "ws1", ID: "telemetry", Type: journal.EntryExecOutputChunk}, // dropped: telemetry
		{WorkspaceID: "", ID: "unscoped", Type: journal.EntryRunStarted},          // dropped: no channel
		{WorkspaceID: "ws1", ID: "keep", Type: journal.EntryRunStarted},           // forwarded
	})
	fake.waitForCalls(t, 1)

	// Give any erroneously-forwarded frame a beat to land before asserting.
	time.Sleep(50 * time.Millisecond)
	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d broadcasts, want exactly 1 (only the feed-relevant, scoped entry)", len(calls))
	}
	row, ok := calls[0].payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", calls[0].payload)
	}
	if row["id"] != "keep" {
		t.Errorf("forwarded entry id = %v, want keep", row["id"])
	}
}

// countingHandler counts slog records at Warn level so the drop-log throttle is
// testable without scraping stderr.
type countingHandler struct {
	mu    sync.Mutex
	warns int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.mu.Lock()
		h.warns++
		h.mu.Unlock()
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }
func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.warns
}

// TestJournalWSBridge_DropLogThrottled proves the drop warning is rate-limited
// (one line per interval) rather than once-per-process: a burst of drops logs
// once, and a later drop after the interval logs again.
func TestJournalWSBridge_DropLogThrottled(t *testing.T) {
	h := &countingHandler{}
	b := &journalWSBridge{
		logger: slog.New(h),
		ch:     make(chan journal.Entry), // unbuffered + no drain → every send drops
	}

	// First burst: many drops, but only one warning within the interval.
	for i := 0; i < 1000; i++ {
		b.observe([]journal.Entry{{WorkspaceID: "ws1", ID: strconv.Itoa(i), Type: journal.EntryRunStarted}})
	}
	if got := h.count(); got != 1 {
		t.Fatalf("warns after burst = %d, want 1 (throttled)", got)
	}

	// Simulate the throttle window elapsing, then drop again: a new line.
	b.lastDropLog.Store(time.Now().Add(-2 * journalWSDropLogInterval).UnixNano())
	b.observe([]journal.Entry{{WorkspaceID: "ws1", ID: "later", Type: journal.EntryRunStarted}})
	if got := h.count(); got != 2 {
		t.Fatalf("warns after window elapsed = %d, want 2 (recurring)", got)
	}
}
