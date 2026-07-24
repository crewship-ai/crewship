package notifyroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/notify"
)

// Recovery tuning. A delivery is retried at most recoveryMaxAttempts times
// (InsertPending starts at attempts=0; each MarkSent/MarkFailed bumps it),
// and only once it has sat untouched for recoveryGraceSecs so a still-in-
// flight first attempt is never double-fired.
const (
	recoveryMaxAttempts = 5
	recoveryGraceSecs   = 60
	recoverySweepLimit  = 200
	recoveryInterval    = 2 * time.Minute
)

// RecoverStuckDeliveries makes the delivery outbox actually survive a
// restart (the durability the v161 outbox was built for but never wired):
// it re-attempts rows left 'pending' by a crash between InsertPending and
// the terminal mark, plus 'failed' rows from a transient dispatch error.
//
// The message body/priority/deep-link are NOT stored on the delivery row,
// so each retry re-derives them from the still-durable inbox_items source
// (same (kind, source_id) the row was minted from). A row whose source or
// channel is gone is marked failed so its attempt count climbs and it ages
// out of the sweep rather than being retried forever.
//
// Returns (attempted, sent). Best-effort: per-row errors are logged and
// skipped, never propagated.
func (r *Router) RecoverStuckDeliveries(ctx context.Context) (attempted, sent int) {
	if r == nil || r.db == nil {
		return 0, 0
	}
	stuck, err := r.deliveries.ListRecoverable(ctx, recoveryMaxAttempts, recoveryGraceSecs, recoverySweepLimit)
	if err != nil {
		r.logger.Warn("notifyroute: recovery: list stuck deliveries", "error", err)
		return 0, 0
	}
	for _, d := range stuck {
		attempted++
		if r.recoverOne(ctx, d) {
			sent++
		}
	}
	if attempted > 0 {
		r.logger.Info("notifyroute: recovery sweep", "attempted", attempted, "sent", sent)
	}
	return attempted, sent
}

// recoverOne re-attempts a single stuck delivery. Returns true iff it was
// delivered this pass.
func (r *Router) recoverOne(ctx context.Context, d Delivery) bool {
	body, priority, url, err := r.deriveMessage(ctx, d.SourceKind, d.SourceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Source inbox item is gone — nothing left to render. Bump the
			// attempt count so the row ages out instead of looping forever.
			_ = r.deliveries.MarkFailed(ctx, d.ID, "recovery: source inbox item no longer exists")
		} else {
			r.logger.Warn("notifyroute: recovery: derive message", "error", err, "delivery_id", d.ID)
		}
		return false
	}

	ch, err := r.channels.GetForDispatch(ctx, d.WorkspaceID, d.ChannelID)
	if err != nil {
		_ = r.deliveries.MarkFailed(ctx, d.ID, "recovery: channel unavailable: "+err.Error())
		return false
	}

	msg := notify.CategoryMessage{
		WorkspaceID: d.WorkspaceID,
		Category:    d.Category,
		Title:       d.Title,
		Body:        body,
		Priority:    priority,
		SourceKind:  d.SourceKind,
		SourceID:    d.SourceID,
		URL:         url,
	}
	if err := r.dispatcher.DeliverCategoryMessage(ctx, ch, msg); err != nil {
		if merr := r.deliveries.MarkFailed(ctx, d.ID, err.Error()); merr != nil {
			r.logger.Warn("notifyroute: recovery: mark failed", "error", merr, "delivery_id", d.ID)
		}
		return false
	}
	if err := r.deliveries.MarkSent(ctx, d.ID); err != nil {
		r.logger.Warn("notifyroute: recovery: mark sent", "error", err, "delivery_id", d.ID)
	}
	return true
}

// deriveMessage re-reads the body/priority/deep-link for a delivery from
// its durable inbox_items source. Returns sql.ErrNoRows if the source is
// gone.
func (r *Router) deriveMessage(ctx context.Context, kind, sourceID string) (body, priority, url string, err error) {
	var payloadJSON string
	err = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(body_md,''), priority, COALESCE(payload_json,'{}')
		 FROM inbox_items WHERE kind = ? AND source_id = ?`,
		kind, sourceID).Scan(&body, &priority, &payloadJSON)
	if err != nil {
		return "", "", "", err
	}
	// Deep link, if the source carried one (same key the live path reads).
	var payload map[string]any
	if json.Unmarshal([]byte(payloadJSON), &payload) == nil {
		if u, ok := payload["chat_url"].(string); ok {
			url = u
		}
	}
	return body, priority, url, nil
}

// RunRecoveryLoop drives RecoverStuckDeliveries on a ticker until ctx is
// cancelled. Wire once at boot (cmd_start.go). One immediate sweep on start
// catches deliveries orphaned by the previous process's crash.
//
// isLeader gates each sweep the same way the cron loops are gated (#1376):
// in a multi-replica deploy only the leader re-delivers, so two replicas
// can't both pick up and double-send the same stuck row (there is no
// per-row claim/lock). A nil predicate means "always sweep" — correct for a
// single replica or when leader election is disabled. Leadership is
// re-checked every tick, so a fail-over promotes the new leader's loop
// without a restart.
func (r *Router) RunRecoveryLoop(ctx context.Context, isLeader func() bool) {
	if r == nil {
		return
	}
	sweep := func() {
		if isLeader != nil && !isLeader() {
			return
		}
		r.RecoverStuckDeliveries(ctx)
	}
	sweep()
	t := time.NewTicker(recoveryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
