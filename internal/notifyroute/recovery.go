package notifyroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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
	body, priority, payload, err := r.deriveMessage(ctx, d.SourceKind, d.SourceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Source inbox item is gone — nothing left to render. Bump the
			// attempt count so the row ages out instead of looping forever.
			// Named per producer: telling someone an inbox item is gone
			// when the delivery never had one makes the loss
			// unattributable.
			source := "inbox item"
			if strings.HasPrefix(d.SourceKind, journalKindPrefix) {
				source = "journal entry"
			}
			_ = r.deliveries.MarkFailed(ctx, d.ID, "recovery: source "+source+" no longer exists")
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

	// Same derivation as the live path, so a recovered delivery carries the
	// same links and variables the original attempt would have.
	links, vars := notificationFacts(d.SourceKind, payload)
	msg := notify.CategoryMessage{
		WorkspaceID: d.WorkspaceID,
		Category:    d.Category,
		Title:       d.Title,
		Body:        body,
		Priority:    priority,
		SourceKind:  d.SourceKind,
		SourceID:    d.SourceID,
		Links:       links,
		Vars:        vars,
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

// deriveMessage re-reads the body, priority and source payload for a delivery
// from its durable inbox_items row. Returns sql.ErrNoRows if the source is
// gone.
//
// It returns the payload rather than a link: turning a payload into links and
// template variables is notificationFacts' job, and doing it here as well is
// what let the live path and this one drift into reading a single hardcoded
// key each.
func (r *Router) deriveMessage(ctx context.Context, kind, sourceID string) (body, priority string, payload map[string]any, err error) {
	// The journal bridge is a producer with NO inbox row — journalItem says
	// so in its own comment — so re-deriving it from inbox_items could only
	// ever return sql.ErrNoRows. Every observational category was therefore
	// non-durable: one transient receiver failure lost the notification for
	// good, while approvals and escalations retried, and the delivery log
	// recorded a reason ("source inbox item no longer exists") that was not
	// true of those rows. The journal is durable by construction; reading it
	// is just reading the source the bridge actually used.
	if strings.HasPrefix(kind, journalKindPrefix) {
		return r.deriveJournalMessage(ctx, sourceID)
	}

	var payloadJSON string
	err = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(body_md,''), priority, COALESCE(payload_json,'{}')
		 FROM inbox_items WHERE kind = ? AND source_id = ?`,
		kind, sourceID).Scan(&body, &priority, &payloadJSON)
	if err != nil {
		return "", "", nil, err
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		// A malformed payload costs links and variables, not the delivery
		// — the message itself is intact.
		r.logger.Warn("notifyroute: recovery: source payload not JSON",
			"error", err, "kind", kind, "source_id", sourceID)
		payload = nil
	}
	return body, priority, payload, nil
}

// deriveJournalMessage rebuilds a journal-sourced notification from its
// journal entry, the same way journalItem built it in the first place — body
// from the entry's own facts, priority from its severity.
//
// sql.ErrNoRows here means the entry has been archived or pruned, which the
// caller ages out exactly as it does for a vanished inbox item; the message it
// records names the journal, because naming an inbox item this delivery never
// had made the loss unattributable.
func (r *Router) deriveJournalMessage(ctx context.Context, entryID string) (string, string, map[string]any, error) {
	var (
		severity    string
		payloadJSON string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT severity, COALESCE(payload,'{}') FROM journal_entries WHERE id = ?`,
		entryID).Scan(&severity, &payloadJSON)
	if err != nil {
		// %w, not a bare return: recoverOne distinguishes sql.ErrNoRows (the
		// entry is gone — age the row out) from a transient read failure
		// (leave it for the next sweep), and wrapping keeps that check
		// working while naming what was being read.
		return "", "", nil, fmt.Errorf("read journal entry %q: %w", entryID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		r.logger.Warn("notifyroute: recovery: journal payload not JSON",
			"error", err, "entry_id", entryID)
		payload = nil
	}
	return journalBody(payload), severityPriority(journal.Severity(severity)), payload, nil
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
	r.recoverySweep(ctx, isLeader)
	t := time.NewTicker(recoveryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.recoverySweep(ctx, isLeader)
		}
	}
}

// recoverySweep is the single leader-gated sweep step, extracted out of
// RunRecoveryLoop's ticker loop so it can be exercised directly by a test —
// a nil/false isLeader must skip the sweep entirely, a true one must run it
// — without waiting on the 2-minute ticker or a background goroutine.
func (r *Router) recoverySweep(ctx context.Context, isLeader func() bool) {
	if isLeader != nil && !isLeader() {
		return
	}
	r.RecoverStuckDeliveries(ctx)
}
