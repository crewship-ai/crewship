package notifyroute

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// ---------------------------------------------------------------------------
// Leader-gate consumption at Router.recoverySweep / RunRecoveryLoop (#1376,
// #1412).
//
// RunRecoveryLoop re-delivers stuck outbox rows on a 2-minute ticker; on a
// multi-replica deploy only the lease holder should sweep, since there's no
// per-row claim/lock — two replicas sweeping the same stuck delivery would
// double-send it. The gate mechanism itself (internal/leader) is well
// tested; what was UNTESTED anywhere in the repo is that the sweep actually
// *consults* the isLeader predicate it's given. Before this file, deleting
//
//	if isLeader != nil && !isLeader() { return }
//
// did not fail a single test. recoverySweep is extracted out of
// RunRecoveryLoop's ticker closure (previously a local, unreachable-outside
// -the-loop func) specifically so it can be called directly here — isLeader
// was already an injectable func() bool parameter, so no other production
// behaviour changed.
// ---------------------------------------------------------------------------

func TestRecoverySweep_NoopWhenNotLeader(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)
	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx-lg-1', 'ws1', 'waitpoint', 'wp-lg-1', 'Approve', 'please approve', 'unread', 'high')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}
	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-lg-1", notify.CategoryAgentsApproval, StatusPending)

	r.recoverySweep(context.Background(), func() bool { return false })

	if got := rs.count(); got != 0 {
		t.Fatalf("webhook posts while not leader = %d, want 0 — the leader gate was not consulted", got)
	}
	if status, _ := deliveryStatus(t, r, id); status != StatusPending {
		t.Errorf("delivery status = %q while not leader, want unchanged %q", status, StatusPending)
	}
}

func TestRecoverySweep_FiresWhenLeader(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)
	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx-lg-2', 'ws1', 'waitpoint', 'wp-lg-2', 'Approve', 'please approve', 'unread', 'high')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}
	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-lg-2", notify.CategoryAgentsApproval, StatusPending)

	r.recoverySweep(context.Background(), func() bool { return true })

	if got := rs.count(); got != 1 {
		t.Fatalf("webhook posts while leader = %d, want exactly 1", got)
	}
	if status, _ := deliveryStatus(t, r, id); status != StatusSent {
		t.Errorf("delivery status = %q while leader, want %q", status, StatusSent)
	}
}
