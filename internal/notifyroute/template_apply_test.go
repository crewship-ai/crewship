package notifyroute

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

// Where an operator's wording actually takes effect.
//
// Templates apply to the notifications the PRODUCT computes — a journal entry
// projected into a message, a scheduler alert — and never to one somebody
// wrote. A routine's notify step and an agent's chat reply carry an author's
// words; rewriting those would be a different feature with a worse name, and
// both arrive as inbox kind "message", which is what the rule keys on.

// lastPost returns the most recent webhook body the recorder saw.
func (rs *recordingWebhookServer) lastPost(t *testing.T) map[string]any {
	t.Helper()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.posts) == 0 {
		t.Fatal("no webhook post recorded")
	}
	return rs.posts[len(rs.posts)-1]
}

func seedTemplate(t *testing.T, db *sql.DB, category, channelID, title, body string) {
	t.Helper()
	if err := notify.NewTemplateStore(db).Upsert(context.Background(), "ws1", notify.MessageTemplate{
		Category: category, ChannelID: channelID, Title: title, Body: body,
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
}

func seedImmediatePref(t *testing.T, db *sql.DB, category, channelID string) {
	t.Helper()
	if err := NewPrefStore(db).Set(context.Background(), "ws1", "u_member",
		[]PrefCell{{Category: category, ChannelID: channelID, State: "immediate"}}); err != nil {
		t.Fatalf("seed pref: %v", err)
	}
}

func TestTemplate_RewritesAComputedNotification(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryRoutinesCompleted, ch.ID)
	seedTemplate(t, db, notify.CategoryRoutinesCompleted, "",
		"{{ vars.pipeline_slug }} is done", "took {{ vars.total_duration_ms }}ms")

	r := newTestRouter(db, nil, nil)
	item := journalItem(journal.Entry{
		ID: "je_1", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted,
		Severity: journal.SeverityInfo, Summary: "Pipeline nightly completed",
		Payload: map[string]any{"pipeline_slug": "nightly", "total_duration_ms": 1200},
	}, notify.CategoryRoutinesCompleted)
	r.route(context.Background(), notify.CategoryRoutinesCompleted, item)

	body := rs.lastPost(t)
	if got, _ := body["title"].(string); got != "nightly is done" {
		t.Errorf("title = %q, want the template's", got)
	}
	if got, _ := body["body"].(string); got != "took 1200ms" {
		t.Errorf("body = %q, want the template's", got)
	}
}

func TestTemplate_LeavesAnAuthoredMessageAlone(t *testing.T) {
	// A routine's notify step wrote this. An operator's category template
	// must not overwrite what the routine's author chose to say.
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryChatReplies, ch.ID)
	seedTemplate(t, db, notify.CategoryChatReplies, "", "OVERWRITTEN", "OVERWRITTEN")

	r := newTestRouter(db, nil, nil)
	r.route(context.Background(), notify.CategoryChatReplies, inbox.Item{
		WorkspaceID: "ws1", Kind: inbox.KindMessage, SourceID: "run_1:report",
		Title: "Fetch and report", BodyMD: "example.com resolves to 1.2.3.4",
		Priority: "low",
		Payload:  map[string]any{"subkind": "routine_update", "pipeline_run_id": "run_1"},
	})

	body := rs.lastPost(t)
	if got, _ := body["title"].(string); got != "Fetch and report" {
		t.Errorf("title = %q — an authored message must survive templating", got)
	}
	if got, _ := body["body"].(string); !strings.Contains(got, "1.2.3.4") {
		t.Errorf("body = %q — an authored message must survive templating", got)
	}
}

func TestTemplate_NoTemplateChangesNothing(t *testing.T) {
	// The path almost every notification takes.
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryRoutinesCompleted, ch.ID)

	r := newTestRouter(db, nil, nil)
	item := journalItem(journal.Entry{
		ID: "je_2", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted,
		Severity: journal.SeverityInfo, Summary: "Pipeline nightly completed",
		Payload: map[string]any{"pipeline_slug": "nightly"},
	}, notify.CategoryRoutinesCompleted)
	r.route(context.Background(), notify.CategoryRoutinesCompleted, item)

	if got, _ := rs.lastPost(t)["title"].(string); got != "Pipeline nightly completed" {
		t.Errorf("title = %q, want the producer's", got)
	}
}

func TestTemplate_ChannelSpecificWins(t *testing.T) {
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryRoutinesCompleted, ch.ID)
	seedTemplate(t, db, notify.CategoryRoutinesCompleted, "", "general", "")
	seedTemplate(t, db, notify.CategoryRoutinesCompleted, ch.ID, "for this channel", "")

	r := newTestRouter(db, nil, nil)
	item := journalItem(journal.Entry{
		ID: "je_3", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted,
		Severity: journal.SeverityInfo, Summary: "Pipeline nightly completed",
		Payload: map[string]any{"pipeline_slug": "nightly"},
	}, notify.CategoryRoutinesCompleted)
	r.route(context.Background(), notify.CategoryRoutinesCompleted, item)

	if got, _ := rs.lastPost(t)["title"].(string); got != "for this channel" {
		t.Errorf("title = %q, want the channel-specific template", got)
	}
}

func TestTemplate_ATemplatedTitleIsStillScrubbed(t *testing.T) {
	// A template can pull any fact into the title, including one holding a
	// secret. Applying it BEFORE delivery is what keeps the envelope scrub
	// covering it — a template must not become a way around redaction.
	const secret = "sk-ant-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryRoutinesCompleted, ch.ID)
	seedTemplate(t, db, notify.CategoryRoutinesCompleted, "", "key {{ vars.token }}", "")

	r := newTestRouter(db, nil, nil)
	item := journalItem(journal.Entry{
		ID: "je_4", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted,
		Severity: journal.SeverityInfo, Summary: "Pipeline nightly completed",
		Payload: map[string]any{"token": secret},
	}, notify.CategoryRoutinesCompleted)
	r.route(context.Background(), notify.CategoryRoutinesCompleted, item)

	if got, _ := rs.lastPost(t)["title"].(string); strings.Contains(got, secret) {
		t.Errorf("a templated title escaped redaction: %q", got)
	}
}

func TestTemplate_TheDeliveryLogRecordsWhatWasActuallySent(t *testing.T) {
	// The outbox row and the Activity timeline both carry a title, and both
	// were filled from the PRODUCER's title — captured before the template
	// ran. So an operator asking "why did that message say something else?"
	// would read a log showing the wording they did not receive, which is
	// the one question this log exists to answer.
	db := newRouteTestDB(t)
	rs := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, rs.URL)
	seedImmediatePref(t, db, notify.CategoryRoutinesCompleted, ch.ID)
	seedTemplate(t, db, notify.CategoryRoutinesCompleted, "", "{{ vars.pipeline_slug }} is done", "")

	r := newTestRouter(db, nil, nil)
	item := journalItem(journal.Entry{
		ID: "je_5", WorkspaceID: "ws1", Type: journal.EntryPipelineRunCompleted,
		Severity: journal.SeverityInfo, Summary: "Pipeline nightly completed",
		Payload: map[string]any{"pipeline_slug": "nightly"},
	}, notify.CategoryRoutinesCompleted)
	r.route(context.Background(), notify.CategoryRoutinesCompleted, item)

	rows, err := NewDeliveryStore(db).List(context.Background(), "ws1", ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 delivery row, got %d", len(rows))
	}
	if rows[0].Title != "nightly is done" {
		t.Errorf("logged title = %q, want what the recipient actually saw", rows[0].Title)
	}
}
