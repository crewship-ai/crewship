package notifyroute

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// insertStuckDelivery writes a notification_deliveries row and back-dates its
// updated_at past the recovery grace window so the sweep will pick it up.
func insertStuckDelivery(t *testing.T, r *Router, ch notify.Channel, kind, sourceID, category, status string) string {
	t.Helper()
	ctx := context.Background()
	d := Delivery{
		WorkspaceID: "ws1", ChannelID: ch.ID, UserID: "u_member",
		Category: category, DedupKey: category + ":" + sourceID,
		SourceKind: kind, SourceID: sourceID, Title: "Approve",
	}
	id, created, err := r.deliveries.InsertPending(ctx, d)
	if err != nil || !created {
		t.Fatalf("insert stuck delivery: id=%s created=%v err=%v", id, created, err)
	}
	// Back-date past the grace window and force the target status.
	if _, err := r.db.ExecContext(ctx,
		`UPDATE notification_deliveries SET status = ?, updated_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`,
		status, id); err != nil {
		t.Fatalf("backdate delivery: %v", err)
	}
	return id
}

func deliveryStatus(t *testing.T, r *Router, id string) (status string, attempts int) {
	t.Helper()
	if err := r.db.QueryRowContext(context.Background(),
		`SELECT status, attempts FROM notification_deliveries WHERE id = ?`, id).
		Scan(&status, &attempts); err != nil {
		t.Fatalf("read delivery %s: %v", id, err)
	}
	return
}

// TestRecovery_RedeliversStuckPending proves the outbox actually survives a
// restart: a row left 'pending' by a crash between InsertPending and the
// terminal mark is re-derived from its inbox source and delivered on the
// next sweep — exactly the durability the v161 outbox comment claims.
func TestRecovery_RedeliversStuckPending(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)

	// Durable source the recovery path re-reads body/priority from.
	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx1', 'ws1', 'waitpoint', 'wp-rec-1', 'Approve', 'please approve', 'unread', 'high')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}

	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-rec-1", notify.CategoryApprovals, StatusPending)

	attempted, sent := r.RecoverStuckDeliveries(context.Background())
	if attempted != 1 || sent != 1 {
		t.Fatalf("recovery: attempted=%d sent=%d, want 1/1", attempted, sent)
	}
	if got := rs.count(); got != 1 {
		t.Errorf("recovery must actually deliver to the channel: got %d posts, want 1", got)
	}
	if status, _ := deliveryStatus(t, r, id); status != StatusSent {
		t.Errorf("recovered delivery status = %q, want sent", status)
	}
}

// TestRecovery_RetriesFailed proves a transient dispatch failure is retried
// (not left dead) once the source is reachable again.
func TestRecovery_RetriesFailed(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)
	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx2', 'ws1', 'waitpoint', 'wp-rec-2', 'Approve', 'body', 'unread', 'medium')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}
	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-rec-2", notify.CategoryApprovals, StatusFailed)

	if _, sent := r.RecoverStuckDeliveries(context.Background()); sent != 1 {
		t.Fatalf("failed delivery should be retried and sent, sent=%d", sent)
	}
	if status, _ := deliveryStatus(t, r, id); status != StatusSent {
		t.Errorf("retried delivery status = %q, want sent", status)
	}
}

// TestRecovery_SkipsOverAttemptCap proves a persistently-failing row stops
// being retried once it hits the attempt cap, instead of looping forever.
func TestRecovery_SkipsOverAttemptCap(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)
	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx3', 'ws1', 'waitpoint', 'wp-rec-3', 'Approve', 'body', 'unread', 'medium')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}
	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-rec-3", notify.CategoryApprovals, StatusFailed)
	// Push attempts to the cap.
	if _, err := db.Exec(`UPDATE notification_deliveries SET attempts = ? WHERE id = ?`, recoveryMaxAttempts, id); err != nil {
		t.Fatalf("bump attempts: %v", err)
	}

	attempted, _ := r.RecoverStuckDeliveries(context.Background())
	if attempted != 0 {
		t.Errorf("row at attempt cap must not be retried, attempted=%d", attempted)
	}
	if got := rs.count(); got != 0 {
		t.Errorf("capped row must not deliver, got %d posts", got)
	}
}

// TestRecovery_SourceGoneAgesOut proves a delivery whose inbox source has
// been deleted is failed (attempt bumped) rather than retried forever.
func TestRecovery_SourceGoneAgesOut(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)
	// No inbox_items row seeded — the source is gone.
	id := insertStuckDelivery(t, r, ch, "waitpoint", "wp-missing", notify.CategoryApprovals, StatusPending)

	_, sent := r.RecoverStuckDeliveries(context.Background())
	if sent != 0 {
		t.Errorf("delivery with no source must not be sent, sent=%d", sent)
	}
	if got := rs.count(); got != 0 {
		t.Errorf("no delivery should occur, got %d posts", got)
	}
	status, attempts := deliveryStatus(t, r, id)
	if status != StatusFailed || attempts == 0 {
		t.Errorf("source-gone row must be marked failed with a bumped attempt count, got status=%q attempts=%d", status, attempts)
	}
}
