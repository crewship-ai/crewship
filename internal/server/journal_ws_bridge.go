package server

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// journalWSBridgeBuffer bounds how many committed entries can be queued for
// WebSocket fan-out before the bridge starts dropping. Sized generously: a
// burst of journal writes (a busy mission) shouldn't drop, but a wedged hub
// must never back-pressure the journal write path.
const journalWSBridgeBuffer = 4096

// journalWSChannelPrefix is the dedicated, OPT-IN channel family the bridge
// fans out on: `journal:{workspaceId}`. It is deliberately NOT the
// `workspace:{id}` channel every dashboard client auto-subscribes to — routing
// the journal there would push every committed entry to every open tab whether
// or not it renders the journal, i.e. an unconditional firehose. Only a client
// that explicitly subscribes to `journal:{workspaceId}` (see the channel
// authorizer's "journal" case, gated on workspace membership) pays for it.
const journalWSChannelPrefix = "journal"

// journalWSDropLogInterval throttles the "buffer full" warning so sustained
// backpressure keeps surfacing (with a running drop count) instead of logging
// exactly once per process and then going silent forever.
const journalWSDropLogInterval = 30 * time.Second

// journalBroadcaster is the slice of ws.Hub the bridge needs. Depending on the
// interface (rather than *ws.Hub) keeps the bridge's fan-out target verifiable
// in a unit test without standing up an authenticated WebSocket. *ws.Hub
// satisfies it; its BroadcastChannel is nil-safe, so a nil hub is a valid,
// no-op sink.
type journalBroadcaster interface {
	BroadcastChannel(prefix, id, eventType string, payload any)
}

// journalWSBridge forwards durably-committed journal entries to the WebSocket
// hub as `journal.entry` events on the OPT-IN `journal:{workspaceId}` channel.
//
// Two design constraints shape it:
//
//  1. It must never back-pressure the journal WRITE path. The commit observer
//     (observe) runs inline on that path, so it only does non-blocking channel
//     sends into a buffered queue; a dedicated goroutine (run) drains that
//     queue into the hub. The hub's own enqueue (ws.Hub.Broadcast) is a send
//     into a buffered (256-deep) channel that only blocks once that queue
//     backs up — keeping it off the write path via the drain goroutine means
//     even that bounded stall can't reach a journal commit. Under sustained
//     backpressure the bridge drops frames; clients reconcile through the SSE
//     stream's Last-Event-ID replay or a /api/v1/journal refetch.
//
//  2. It must not be a firehose. Fan-out is gated on the opt-in journal
//     channel (above) AND filtered to feed-relevant entry types
//     (journal.IsFeedRelevant) so high-frequency telemetry — streamed exec
//     output, per-sample container metrics, tracing spans — never hits the
//     wire even for subscribed clients.
type journalWSBridge struct {
	hub    journalBroadcaster
	logger *slog.Logger
	ch     chan journal.Entry

	// Dropped-frame accounting for the throttled warning. dropped counts
	// frames shed since the last log; lastDropLog is the Unix-nano timestamp
	// of that log. Both are touched only from observe today (single write
	// path), but kept atomic so a future multi-producer change stays
	// race-free.
	dropped     atomic.Uint64
	lastDropLog atomic.Int64
}

func newJournalWSBridge(hub *ws.Hub, logger *slog.Logger) *journalWSBridge {
	return newJournalWSBridgeWith(hub, logger)
}

// newJournalWSBridgeWith is the interface-typed constructor the production
// entry point delegates to; tests inject a fake broadcaster through it.
func newJournalWSBridgeWith(hub journalBroadcaster, logger *slog.Logger) *journalWSBridge {
	if logger == nil {
		logger = slog.Default()
	}
	b := &journalWSBridge{
		hub:    hub,
		logger: logger,
		ch:     make(chan journal.Entry, journalWSBridgeBuffer),
	}
	go b.run()
	return b
}

// observe is registered as the journal Writer's commit observer. It runs
// inline on the persist path, so it MUST stay cheap: skip entries with no
// workspace channel to reach and high-frequency telemetry, then value-copy the
// rest onto the buffered channel with a non-blocking send, dropping if full. It
// never touches the hub directly (that would risk blocking the write path).
func (b *journalWSBridge) observe(entries []journal.Entry) {
	for i := range entries {
		e := entries[i]
		if e.WorkspaceID == "" {
			continue // unscoped entries have no workspace channel to reach
		}
		if !journal.IsFeedRelevant(e.Type) {
			continue // high-frequency telemetry — never on the realtime feed
		}
		select {
		case b.ch <- e:
		default:
			// Buffer full — drop this frame. Realtime is best-effort; the
			// durable row is already committed and reachable via the journal
			// API / SSE replay. Log at a throttled cadence (not once-per-
			// process) so a chronically full buffer stays visible.
			b.noteDrop()
		}
	}
}

// noteDrop records a shed frame and emits a rate-limited warning carrying the
// number of frames dropped since the previous log, so sustained backpressure
// is observable without flooding the log on every dropped frame.
func (b *journalWSBridge) noteDrop() {
	b.dropped.Add(1)
	now := time.Now().UnixNano()
	last := b.lastDropLog.Load()
	if now-last < int64(journalWSDropLogInterval) {
		return
	}
	// Only the goroutine that wins the CAS logs and resets the window, so a
	// burst of drops produces one line per interval rather than one per frame.
	if !b.lastDropLog.CompareAndSwap(last, now) {
		return
	}
	n := b.dropped.Swap(0)
	b.logger.Warn("journal→WS bridge buffer full; dropping live frames (clients reconcile via SSE replay / refetch)",
		"buffer", journalWSBridgeBuffer,
		"dropped_since_last_log", n,
		"log_interval", journalWSDropLogInterval)
}

// run drains committed entries to the hub. Serializing here (off the write
// path) keeps the observer cheap. The channel is never closed — the bridge
// lives for the process lifetime, so a late shutdown-drain commit can still
// enqueue without a send-on-closed-channel panic.
func (b *journalWSBridge) run() {
	for e := range b.ch {
		b.hub.BroadcastChannel(journalWSChannelPrefix, e.WorkspaceID, "journal.entry", journal.SerializeEntry(e))
	}
}
