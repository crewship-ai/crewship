package notifyroute

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

type lifecycleJournal struct {
	entries chan journal.Entry
	err     error
}

func (j lifecycleJournal) Emit(_ context.Context, e journal.Entry) (string, error) {
	j.entries <- e
	return "recorded", j.err
}

func TestObservedJournalRoutesCommittedFactWithoutRetainingEntrySlice(t *testing.T) {
	db := newRouteTestDB(t)
	server := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	channel := seedWebhookChannel(t, db, server.URL)
	if err := r.prefs.Set(t.Context(), "ws1", "u_member", []PrefCell{{Category: notify.CategoryRoutinesCompleted, ChannelID: channel.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	sink := lifecycleJournal{entries: make(chan journal.Entry, 4)}
	r.SetJournal(sink)
	entries := []journal.Entry{
		{ID: "self-notification", WorkspaceID: "ws1", Type: journal.EntryNotificationDelivered},
		{ID: "unscoped", Type: journal.EntryPipelineRunCompleted},
		{ID: "committed-event", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted, Summary: "Routine finished", Severity: journal.SeverityInfo, Payload: map[string]any{"pipeline_slug": "nightly"}},
	}
	r.ObserveJournal(entries)
	for i := range entries {
		entries[i] = journal.Entry{Summary: "reused writer buffer"}
	}
	select {
	case event := <-sink.entries:
		if event.Type != journal.EntryNotificationDelivered || event.WorkspaceID != "ws1" || event.Payload["title"] != "Routine finished" {
			t.Fatalf("journal route lost committed value: %+v", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("journal fact never delivered")
	}
	if server.count() != 1 {
		t.Fatalf("unexpected delivery count: %d", server.count())
	}
	rows, err := r.deliveries.List(t.Context(), "ws1", ListFilter{})
	if err != nil || len(rows) != 1 || rows[0].SourceID != "committed-event" || rows[0].Status != StatusSent {
		t.Fatalf("outbox=%+v %v", rows, err)
	}
	var disabled *Router
	disabled.ObserveJournal(nil)
	(&Router{}).ObserveJournal(nil)
}

func TestDeliveryJournalScrubsEveryOutcomeAndPreservesScope(t *testing.T) {
	secret := "sk-ant-" + rand.Text() + rand.Text()
	for _, outcome := range []struct {
		kind     journal.EntryType
		word     string
		provider string
		detail   string
		fail     bool
	}{
		{journal.EntryNotificationDelivered, "Sent", "", "", false},
		{journal.EntryNotificationFailed, "Failed", "chat-provider", "failure " + secret, false},
		{journal.EntryNotificationDropped, "Dropped", "", "rate limit", false},
		{journal.EntryNotificationFailed, "Failed", "", "failure " + secret, true},
	} {
		t.Run(outcome.word+outcome.provider+outcome.detail[:min(3, len(outcome.detail))], func(t *testing.T) {
			r := NewRouter(nil, nil, nil, nil, nil)
			sink := lifecycleJournal{entries: make(chan journal.Entry, 1)}
			if outcome.fail {
				sink.err = errors.New("journal unavailable")
			}
			r.SetJournal(sink)
			channel := notify.Channel{ID: "channel", WorkspaceID: "workspace", Type: notify.ChannelWebhook, Provider: outcome.provider}
			r.emitDeliveryJournal(t.Context(), outcome.kind, journal.SeverityNotice, channel, notify.CategorySystemHealth, "diagnostic "+secret, outcome.detail)
			event := <-sink.entries
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "REDACTED") {
				t.Fatal("journal leaked an outbound credential")
			}
			if event.WorkspaceID != "workspace" || event.ActorID != "notify" || event.ActorType != journal.ActorSystem || !strings.HasPrefix(event.Summary, outcome.word) {
				t.Fatalf("misattributed delivery: %+v", event)
			}
			target := outcome.provider
			if target == "" {
				target = string(notify.ChannelWebhook)
			}
			if event.Payload["target"] != target || event.Payload["channel_id"] != "channel" {
				t.Fatalf("incorrect destination evidence: %+v", event.Payload)
			}
			_, detail := event.Payload["detail"]
			if detail != (outcome.detail != "") {
				t.Fatalf("detail presence=%v", detail)
			}
		})
	}
}

func TestDeliveryStorageFailureNeverBecomesAcknowledgedOrEmptySuccess(t *testing.T) {
	db := newRouteTestDB(t)
	r := newTestRouter(db, nil, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	d := Delivery{WorkspaceID: "ws1", ChannelID: "channel", Category: notify.CategorySystemHealth, DedupKey: "event"}
	if _, created, err := r.deliveries.InsertPending(t.Context(), d); err == nil || created {
		t.Fatalf("uncommitted outbox accepted: %v %v", created, err)
	}
	for _, err := range []error{r.deliveries.InsertDropped(t.Context(), d, StatusDroppedRate), r.deliveries.MarkSent(t.Context(), "delivery"), r.deliveries.MarkFailed(t.Context(), "delivery", "offline")} {
		if err == nil {
			t.Fatal("unwritten delivery outcome accepted")
		}
	}
	if _, err := r.deliveries.List(t.Context(), "ws1", ListFilter{}); err == nil {
		t.Fatal("unreadable delivery log became empty")
	}
	if _, err := r.deliveries.ListRecoverable(t.Context(), 5, 60, 0); err == nil {
		t.Fatal("unreadable outbox became drained")
	}
	if _, err := r.prefs.Get(t.Context(), "ws1", "u_member"); err == nil {
		t.Fatal("unreadable preferences became defaults")
	}
	if err := r.prefs.Set(t.Context(), "ws1", "u_member", nil); err == nil {
		t.Fatal("unwritten preferences accepted")
	}
	if attempted, sent := r.RecoverStuckDeliveries(t.Context()); attempted != 0 || sent != 0 {
		t.Fatalf("unreadable recovery claimed work: %d/%d", attempted, sent)
	}
	for _, item := range []inbox.Item{{WorkspaceID: "ws1"}, {WorkspaceID: "ws1", TargetRole: "OWNER"}, {WorkspaceID: "ws1", TargetUserID: "u_member"}} {
		r.route(t.Context(), notify.CategorySystemHealth, item)
	}
	var disabled *Router
	if attempted, sent := disabled.RecoverStuckDeliveries(t.Context()); attempted != 0 || sent != 0 {
		t.Fatal("disabled recovery claimed work")
	}
}

func TestPreferenceBatchRollsBackWhenOneCellCannotPersist(t *testing.T) {
	db := newRouteTestDB(t)
	server := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, server.URL)
	prefs := NewPrefStore(db)
	if _, err := db.Exec(`CREATE TRIGGER reject_pref BEFORE INSERT ON user_notification_prefs WHEN NEW.category='system.health' BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	cells := []PrefCell{{Category: notify.CategoryRoutinesCompleted, ChannelID: ch.ID, State: "immediate"}, {Category: notify.CategorySystemHealth, ChannelID: ch.ID, State: "immediate"}}
	if err := prefs.Set(t.Context(), "ws1", "u_member", cells); err == nil {
		t.Fatal("partial preference batch accepted")
	}
	got, err := prefs.Get(t.Context(), "ws1", "u_member")
	if err != nil || len(got) != 0 {
		t.Fatalf("partial opt-in persisted: %v %v", got, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_pref`); err != nil {
		t.Fatal(err)
	}
	if err := prefs.Set(t.Context(), "ws1", "u_member", cells); err != nil {
		t.Fatal(err)
	}
	got, err = prefs.Get(t.Context(), "ws1", "u_member")
	if err != nil || len(got) != 2 {
		t.Fatalf("retry failed: %v %v", got, err)
	}
	if _, err := db.Exec(`ALTER TABLE user_notification_prefs RENAME TO unavailable_prefs`); err != nil {
		t.Fatal(err)
	}
	if err := prefs.Set(t.Context(), "ws1", "u_member", cells); err == nil {
		t.Fatal("missing table accepted prefs")
	}
}

func TestRecoveryLoopImmediatelyDrainsAndStopsOnCancellation(t *testing.T) {
	db := newRouteTestDB(t)
	server := newRecordingWebhookServer(t)
	r := newTestRouter(db, nil, nil)
	ch := seedWebhookChannel(t, db, server.URL)
	insertJournalEntry(t, r, "restart", "pipeline.run.completed", "info", "Routine completed", `{}`)
	id := insertStuckDelivery(t, r, ch, "journal:pipeline.run.completed", "restart", notify.CategoryRoutinesCompleted, StatusPending)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.RunRecoveryLoop(ctx, func() bool { return true }) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		status, _ := deliveryStatus(t, r, id)
		if status == StatusSent {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("startup did not recover before the two-minute timer")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop")
	}
	if server.count() != 1 {
		t.Fatalf("startup recovery duplicated delivery: %d", server.count())
	}
	var disabled *Router
	disabled.RunRecoveryLoop(ctx, nil)
}

func TestJournalFactsOmitBlankValuesAndPrivateIdentifiers(t *testing.T) {
	e := journal.Entry{ID: "event", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted, Summary: "Completed", Payload: map[string]any{"private_id": "hidden", "empty": " \n "}}
	item := journalItem(e, notify.CategoryRoutinesCompleted)
	if strings.Contains(item.BodyMD, "hidden") || strings.Contains(item.BodyMD, "empty") {
		t.Fatalf("non-facts leaked into body: %q", item.BodyMD)
	}
}
