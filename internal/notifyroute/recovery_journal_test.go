package notifyroute

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// The recovery sweep is what makes the delivery outbox durable, and it could
// not recover anything the journal bridge produced.
//
// deriveMessage re-read the source with
// `SELECT ... FROM inbox_items WHERE kind = ? AND source_id = ?`, but
// journalItem mints Kind = "journal:"+entry type and SourceID = the journal
// entry id, and writes nothing to inbox_items — its own comment says so.
// deliverToChannel still recorded a real notification_deliveries row, so a
// journal-sourced delivery that failed transiently (a receiver 503ing for
// thirty seconds) entered the recoverable set, hit sql.ErrNoRows on every
// pass, and was marked failed with "recovery: source inbox item no longer
// exists" until it aged out.
//
// Two costs. Every observational category — issues.*, agents.error,
// agents.budget, system.*, security, routines.completed — was silently
// non-durable while approvals and escalations retried. And the reason
// recorded in the admin Deliveries view was factually wrong for those rows,
// so the loss could not even be attributed.
//
// The journal is durable by construction; it is the audit log. Recovering
// from it is a matter of reading the source the bridge actually used.

func insertJournalEntry(t *testing.T, r *Router, id, entryType, severity, summary, payloadJSON string) {
	t.Helper()
	if _, err := r.db.ExecContext(context.Background(),
		`INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, payload)
		 VALUES (?, 'ws1', '2026-07-28T10:00:00.000Z', ?, ?, 'system', ?, ?)`,
		id, entryType, severity, summary, payloadJSON); err != nil {
		t.Fatalf("insert journal entry: %v", err)
	}
}

func TestRecovery_RedeliversAJournalSourcedDelivery(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)

	insertJournalEntry(t, r, "je_1", "pipeline.run.completed", "info",
		"Pipeline nightly completed",
		`{"pipeline_slug":"nightly","total_duration_ms":1200}`)

	id := insertStuckDelivery(t, r,
		ch, "journal:pipeline.run.completed", "je_1", notify.CategoryRoutinesCompleted, StatusFailed)

	attempted, sent := r.RecoverStuckDeliveries(context.Background())
	if attempted != 1 || sent != 1 {
		t.Fatalf("recovery: attempted=%d sent=%d, want 1/1 — a journal-sourced delivery must be recoverable",
			attempted, sent)
	}
	if status, _ := deliveryStatus(t, r, id); status != StatusSent {
		t.Fatalf("status = %q, want sent", status)
	}
	if got := rs.count(); got != 1 {
		t.Errorf("recovery must actually deliver: got %d posts, want 1", got)
	}
}

func TestRecovery_JournalEntryGoneIsReportedHonestly(t *testing.T) {
	// A journal entry that has been archived or pruned genuinely cannot be
	// re-derived. That must age the row out — as before — but the reason
	// recorded has to name the source that was actually missing, not an
	// inbox item this delivery never had.
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)

	id := insertStuckDelivery(t, r,
		ch, "journal:pipeline.run.completed", "je_missing", notify.CategoryRoutinesCompleted, StatusFailed)

	r.RecoverStuckDeliveries(context.Background())

	var errText string
	if err := r.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(error,'') FROM notification_deliveries WHERE id = ?`, id).Scan(&errText); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if strings.Contains(errText, "inbox item") {
		t.Errorf("the recorded reason names the wrong source: %q", errText)
	}
	if !strings.Contains(errText, "journal") {
		t.Errorf("the reason should name the journal entry that was missing, got %q", errText)
	}
}

func TestRecovery_InboxSourcedDeliveriesAreUnaffected(t *testing.T) {
	// The branch must not disturb the producer that already worked.
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, rs.URL)

	if _, err := db.Exec(
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md, state, priority)
		 VALUES ('ibx_j1', 'ws1', 'waitpoint', 'tok_1', 'Approve', 'please approve', 'unread', 'high')`); err != nil {
		t.Fatalf("seed inbox source: %v", err)
	}
	id := insertStuckDelivery(t, r, ch, "waitpoint", "tok_1", notify.CategoryAgentsApproval, StatusFailed)

	r.RecoverStuckDeliveries(context.Background())

	if status, _ := deliveryStatus(t, r, id); status != StatusSent {
		t.Fatalf("status = %q, want sent", status)
	}
}
