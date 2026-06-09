package chatbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/memory"
)

// steerMetadataKind tags a persisted steering message in conversation
// history so the next turn's context builder (and the UI transcript) can
// tell a queued mid-turn steer apart from an ordinary user message.
const steerMetadataKind = "queued_steer"

// steerEventType is the WS/status event name announced on the chat's
// session channel when a steering message is queued. Underscored form,
// consistent with the other lifecycle events the chat surface consumes.
const steerEventType = "steering_queued"

// SteerBroadcaster announces steering_queued events. *ws.Hub satisfies it
// via BroadcastChannel("session", chatID, ...). Kept as a one-method
// interface (not the concrete hub) so chatbridge stays free of a ws
// import cycle and tests can record calls without a live socket.
type SteerBroadcaster interface {
	BroadcastChannel(prefix, id, eventType string, payload any)
}

// SetSteerBroadcaster wires the WS hub used to announce steering_queued.
// Optional — nil leaves the announcement off; the durable persist still
// happens. Done as a setter for the same boot-order reason as
// SetProvisioningEnqueuer.
func (b *Bridge) SetSteerBroadcaster(bc SteerBroadcaster) {
	b.steerBroadcaster = bc
}

// markRunStart records that a run is live for chatID. Counter, not bool,
// so overlapping runs on one chat don't clear the flag prematurely.
func (b *Bridge) markRunStart(chatID string) {
	b.activeRunsMu.Lock()
	b.activeRuns[chatID]++
	b.activeRunsMu.Unlock()
}

// markRunEnd decrements the live-run counter for chatID, deleting the key
// at zero so the map doesn't grow unbounded across chats. Guards against
// underflow so a stray extra call can never wedge a chat "in flight".
func (b *Bridge) markRunEnd(chatID string) {
	b.activeRunsMu.Lock()
	defer b.activeRunsMu.Unlock()
	if b.activeRuns[chatID] <= 1 {
		delete(b.activeRuns, chatID)
		return
	}
	b.activeRuns[chatID]--
}

// runInFlight reports whether at least one run is currently live for chatID.
func (b *Bridge) runInFlight(chatID string) bool {
	b.activeRunsMu.Lock()
	defer b.activeRunsMu.Unlock()
	return b.activeRuns[chatID] > 0
}

// SteerResult reports the outcome of a steering message to the caller
// (the HTTP handler / CLI). Queued is always true on a non-error return
// in this slice — live injection into a running turn is a deferred
// follow-up, so today every steer is queued for the next turn. InFlight
// tells the operator whether a run was live when they steered (i.e.
// whether the message will land after the current turn finishes, or at
// the start of the next turn they kick off).
type SteerResult struct {
	Queued   bool `json:"queued"`
	InFlight bool `json:"in_flight"`
}

// Steer delivers a mid-turn steering message into a chat.
//
// Safe-slice behaviour (live injection deferred — see PR follow-ups):
// the message is QUEUED for the next turn rather than injected into a
// running one. Concretely:
//
//  1. Reject empty/whitespace content up front (no no-op turns).
//  2. Run the text through memory.ScanContent and BLOCK on a hit — a
//     steering channel is an untrusted free-text input into the agent's
//     context, so it gets the same quarantine scan as memory/tool output.
//  3. Persist the message to conversation history tagged in Metadata as a
//     queued steer (kind=queued_steer, in_flight=<live?>), so the next
//     turn's context picks it up like any other user message.
//  4. Announce a steering_queued event on the chat's session channel
//     (best-effort; nil broadcaster skips it).
//
// It NEVER spawns a second Exec/RunAgent: queueing is the whole point of
// the guard. Whether a run was live is reflected in SteerResult.InFlight.
func (b *Bridge) Steer(ctx context.Context, chatID, content string) (SteerResult, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return SteerResult{}, fmt.Errorf("steering content is empty")
	}

	// Quarantine scan BEFORE persistence. A hit blocks the whole
	// operation — nothing is written, nothing is announced.
	if hit := memory.ScanContent(trimmed); hit != nil {
		b.logger.Warn("steering message blocked by content scan",
			"chat_id", chatID, "category", hit.Category, "pattern", hit.Pattern)
		return SteerResult{}, fmt.Errorf("steering message blocked: %s (%s)", hit.Category, hit.Pattern)
	}

	inFlight := b.runInFlight(chatID)

	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:        generateMsgID(),
		Role:      conversation.RoleUser,
		Content:   trimmed,
		Timestamp: time.Now().UTC(),
		Metadata: map[string]any{
			"kind":      steerMetadataKind,
			"in_flight": inFlight,
		},
	}); err != nil {
		b.logger.Error("failed to persist steering message", "chat_id", chatID, "error", err)
		return SteerResult{}, fmt.Errorf("persist steering message: %w", err)
	}

	// Best-effort UI announcement on the session channel.
	if b.steerBroadcaster != nil {
		b.steerBroadcaster.BroadcastChannel("session", chatID, steerEventType, map[string]any{
			"chat_id":   chatID,
			"in_flight": inFlight,
			"content":   trimmed,
		})
	}

	b.logger.Info("steering message queued", "chat_id", chatID, "in_flight", inFlight)
	return SteerResult{Queued: true, InFlight: inFlight}, nil
}
