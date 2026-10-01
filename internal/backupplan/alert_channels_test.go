package backupplan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/notifyroute"
)

// alertHook is a webhook receiver that records what it is sent and answers
// with a settable status.
type alertHook struct {
	*httptest.Server
	mu     sync.Mutex
	posts  []map[string]any
	status atomic.Int32
}

func newAlertHook(t *testing.T) *alertHook {
	t.Helper()
	h := &alertHook{}
	h.status.Store(http.StatusOK)
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		h.mu.Lock()
		h.posts = append(h.posts, body)
		h.mu.Unlock()
		w.WriteHeader(int(h.status.Load()))
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *alertHook) titles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.posts {
		s, _ := p["title"].(string)
		out = append(out, s)
	}
	return out
}

func (h *alertHook) last() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.posts) == 0 {
		return nil
	}
	return h.posts[len(h.posts)-1]
}

// alertRig is the scheduler harness plus a webhook channel in workspace
// Alpha, the alerter, and loopback delivery (the SSRF guard blocks
// 127.0.0.1; allowLoopback false keeps it).
type alertRig struct {
	*harness
	hook    *alertHook
	channel notify.Channel
	alerter *ChannelAlerter
}

func newAlertRig(t *testing.T, allowLoopback bool) *alertRig {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("0123456789abcdef", 4))
	t.Setenv("CREWSHIP_PUBLIC_URL", "")
	if allowLoopback {
		t.Cleanup(notify.SetWebhookTransportForTesting(http.DefaultTransport))
	}
	h := newHarness(t, "2026-09-30T02:00:00Z")
	hook := newAlertHook(t)
	ch, err := notify.NewChannelStore(h.db).Create(context.Background(), notify.ChannelInput{WorkspaceID: "ws_a", Type: notify.ChannelWebhook, URL: hook.URL})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := NewChannelAlerter(h.db, notify.NewDispatcher(notify.NewChannelStore(h.db), nil, logger, h.db), logger)
	a.Now = h.clock.Now
	return &alertRig{harness: h, hook: hook, channel: ch, alerter: a}
}

func (r *alertRig) route(ids ...string) {
	r.t.Helper()
	set := DefaultSettings()
	set.Channels = ids
	if err := SaveSettings(context.Background(), r.db, set, "u1", r.clock.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *alertRig) raise(kind, msg string, bump bool) (*Incident, bool) {
	r.t.Helper()
	inc, opened, err := RaiseIncident(context.Background(), r.db, "", kind, msg, "", r.clock.Now(), bump)
	if err != nil {
		r.t.Fatal(err)
	}
	return inc, opened
}

func (r *alertRig) deliveries() []notifyroute.Delivery {
	r.t.Helper()
	rows, err := r.db.Query(`SELECT dedup_key, status, COALESCE(error,'') FROM notification_deliveries WHERE source_kind = ? ORDER BY created_at, dedup_key`, AlertSourceKind)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []notifyroute.Delivery
	for rows.Next() {
		var d notifyroute.Delivery
		if err := rows.Scan(&d.DedupKey, &d.Status, &d.Error); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// One message per incident state change: opened, each repeat, resolved —
// never one per retry of the same change, and no "resolved" for an incident
// the channel was never told about.
func TestChannelAlerts_OneMessagePerStateChange(t *testing.T) {
	ctx := context.Background()
	type step struct {
		name       string
		do         func(r *alertRig)
		wantTitles []string
	}
	var inc *Incident
	steps := []step{
		{"opened", func(r *alertRig) {
			var opened bool
			inc, opened = r.raise(IncidentFailed, "Manual backup failed. Last successful backup: never.", true)
			r.alerter.Raised(ctx, inc, opened, "disk full")
		}, []string{AlertTitleRaised}},
		{"the same opening again is not sent twice", func(r *alertRig) {
			r.alerter.Raised(ctx, inc, true, "disk full")
		}, []string{AlertTitleRaised}},
		{"a repeat is a new state change", func(r *alertRig) {
			inc, _ = r.raise(IncidentFailed, "Manual backup failed. Last successful backup: never.", true)
			r.alerter.Raised(ctx, inc, false, "disk full")
		}, []string{AlertTitleRaised, AlertTitleRaised}},
		{"the same repeat again is not sent twice", func(r *alertRig) {
			r.alerter.Raised(ctx, inc, false, "disk full")
		}, []string{AlertTitleRaised, AlertTitleRaised}},
		{"resolved", func(r *alertRig) {
			closed, err := ResolveIncidents(ctx, r.db, "", []string{IncidentFailed}, r.clock.Now())
			if err != nil || len(closed) != 1 {
				t.Fatalf("resolve: %v %v", closed, err)
			}
			r.alerter.Resolved(ctx, closed[0])
			r.alerter.Resolved(ctx, closed[0])
		}, []string{AlertTitleRaised, AlertTitleRaised, AlertTitleResolved}},
		{"an incident never announced resolves quietly", func(r *alertRig) {
			quiet, _ := r.raise(IncidentStale, "stale", false)
			quiet.State = "resolved"
			r.alerter.Resolved(ctx, quiet)
		}, []string{AlertTitleRaised, AlertTitleRaised, AlertTitleResolved}},
	}
	r := newAlertRig(t, true)
	r.route(r.channel.ID)
	for _, s := range steps {
		s.do(r)
		r.alerter.Wait()
		if got := r.hook.titles(); strings.Join(got, "|") != strings.Join(s.wantTitles, "|") {
			t.Fatalf("%s: channel got %q, want %q", s.name, got, s.wantTitles)
		}
	}
	var keys []string
	for _, d := range r.deliveries() {
		if d.Status != notifyroute.StatusSent {
			t.Fatalf("%s: %s (%s)", d.DedupKey, d.Status, d.Error)
		}
		keys = append(keys, strings.TrimPrefix(d.DedupKey, AlertSourceKind+":"+inc.ID+":"))
	}
	if strings.Join(keys, ",") != "opened,repeat:2,resolved" {
		t.Fatalf("outbox keys %v", keys)
	}
	body, _ := r.hook.last()["body"].(string)
	if !strings.Contains(body, "Plan: Manual backups") || !strings.Contains(body, "Resolved: ") || !strings.Contains(body, "Last good backup: never") {
		t.Fatalf("resolved body:\n%s", body)
	}
}

// With no channel on the route nothing is written or sent.
func TestChannelAlerts_NoRouteNoDelivery(t *testing.T) {
	r := newAlertRig(t, true)
	inc, opened := r.raise(IncidentFailed, "failed", true)
	r.alerter.Raised(context.Background(), inc, opened, "")
	r.alerter.Wait()
	if n := len(r.hook.titles()); n != 0 || len(r.deliveries()) != 0 {
		t.Fatalf("posts %d, deliveries %v", n, r.deliveries())
	}
}

func TestAlertMessage_Format(t *testing.T) {
	r := newAlertRig(t, true)
	ctx := context.Background()
	p := r.plan(func(p *Plan) { p.Name = "Nightly" })
	end := r.clock.Now().Add(-32 * time.Hour)
	if _, err := r.db.Exec(`INSERT INTO backup_runs (id, plan_id, trigger, scope, status, started_at, ended_at) VALUES ('run_ok', ?, 'schedule', 'workspaces', 'done', ?, ?)`,
		p.ID, ts(end.Add(-time.Minute)), ts(end)); err != nil {
		t.Fatal(err)
	}
	inc, _, err := RaiseIncident(ctx, r.db, p.ID, IncidentFailed, "Nightly backup failed. Last successful backup: 32 hours ago.", "", r.clock.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	inc.Count, inc.FirstAt = 3, "2026-09-28T02:00:00Z"
	cases := []struct {
		event, detail string
		title, prio   string
		want          []string
	}{
		{AlertEventOpened, "", AlertTitleRaised, "high", []string{"Plan: Nightly", "What happened: Nightly backup failed.", "Repeats: 3 times since 2026-09-28T02:00:00Z · one incident", "Last good backup: 32 hours ago"}},
		{alertEventRepeat(3), strings.Repeat("x", 600), AlertTitleRaised, "high", []string{"Detail: " + strings.Repeat("x", 500) + "…"}},
		{AlertEventResolved, "", AlertTitleResolved, "low", []string{"Plan: Nightly", "Resolved: Nightly backup failed.", "It happened 3 times since", "Last good backup: 32 hours ago"}},
	}
	for _, tc := range cases {
		m := AlertMessage(ctx, r.db, inc, tc.event, tc.detail, r.clock.Now())
		if m.Title != tc.title || m.Priority != tc.prio || m.Category != notify.CategorySystemHealth {
			t.Fatalf("%s: title %q priority %q category %q", tc.event, m.Title, m.Priority, m.Category)
		}
		for _, w := range tc.want {
			if !strings.Contains(m.Body, w) {
				t.Fatalf("%s: body lacks %q:\n%s", tc.event, w, m.Body)
			}
		}
		if len(m.Links) != 1 || m.Links[0].Path != AlertLink {
			t.Fatalf("%s: links %v", tc.event, m.Links)
		}
	}
	// The link is made absolute against the instance's public base URL at
	// delivery.
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	r.route(r.channel.ID)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := NewChannelAlerter(r.db, notify.NewDispatcher(notify.NewChannelStore(r.db), nil, logger, r.db), logger)
	a.Raised(ctx, inc, false, "")
	a.Wait()
	if got, _ := r.hook.last()["url"].(string); got != "https://crewship.example.com"+AlertLink {
		t.Fatalf("delivered url %q", got)
	}
}

// A failed delivery is recorded on the outbox row, shows on the Overview,
// and the notification recovery sweep retries it once the receiver is back.
func TestChannelAlerts_FailureIsRecordedAndRetried(t *testing.T) {
	ctx := context.Background()
	r := newAlertRig(t, true)
	r.route(r.channel.ID)
	r.hook.status.Store(http.StatusInternalServerError)
	inc, opened := r.raise(IncidentFailed, "failed", true)
	r.alerter.Raised(ctx, inc, opened, "")
	r.alerter.Wait()

	last := LastAlertDelivery(ctx, r.db, r.channel.ID)
	if last == nil || last.Status != "failed" || last.Error == nil || !strings.Contains(*last.Error, "500") {
		t.Fatalf("last delivery %+v", last)
	}
	items := AlertDeliveryFailures(ctx, r.db)
	if len(items) != 1 || !strings.HasPrefix(items[0].Title, "Backup alerts could not be delivered to Webhook 127.0.0.1") ||
		!strings.HasSuffix(items[0].Title, " · Alpha") || items[0].Action == nil || items[0].Action.Kind != "keys" {
		t.Fatalf("needs attention %+v", items)
	}
	ov, err := BuildOverview(ctx, r.db, OverviewInput{Scope: ScopeInstance}, r.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range ov.NeedsAttention {
		found = found || it.ID == "alerts:"+r.channel.ID
	}
	if !found {
		t.Fatalf("overview needs_attention lacks the delivery failure: %+v", ov.NeedsAttention)
	}

	// The receiver recovers; the outbox sweep delivers the same change once.
	r.hook.status.Store(http.StatusOK)
	if _, err := r.db.Exec(`UPDATE notification_deliveries SET updated_at = '2020-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	router := notifyroute.NewRouter(r.db, notify.NewDispatcher(notify.NewChannelStore(r.db), nil, r.svc.Logger, r.db), nil, nil, r.svc.Logger)
	if _, sent := router.RecoverStuckDeliveries(ctx); sent != 1 {
		t.Fatalf("recovery sent %d, want 1", sent)
	}
	if last := LastAlertDelivery(ctx, r.db, r.channel.ID); last == nil || last.Status != "sent" {
		t.Fatalf("after recovery %+v", last)
	}
	if items := AlertDeliveryFailures(ctx, r.db); len(items) != 0 {
		t.Fatalf("a delivered alert still needs attention: %+v", items)
	}
	if n := len(r.hook.titles()); n != 2 {
		t.Fatalf("receiver saw %d posts, want the failed one and the retry", n)
	}
}

// The recovery sweep gives up an alert that no longer applies.
func TestChannelAlerts_RecoveryDropsWhatNoLongerApplies(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		after func(r *alertRig, inc *Incident)
		want  string
	}{
		{"the incident was resolved", func(r *alertRig, inc *Incident) {
			if _, err := ResolveIncidents(ctx, r.db, "", []string{inc.Kind}, r.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, "recovery: the backup incident was resolved before this alert went out"},
		{"the channel left the route", func(r *alertRig, _ *Incident) { r.route() }, "recovery: the channel is no longer on the backup alert route"},
		{"the channel was switched off", func(r *alertRig, _ *Incident) {
			if _, err := r.db.Exec(`UPDATE notification_channels SET enabled = 0`); err != nil {
				t.Fatal(err)
			}
		}, "recovery: the channel is switched off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newAlertRig(t, true)
			r.route(r.channel.ID)
			r.hook.status.Store(http.StatusBadGateway)
			inc, opened := r.raise(IncidentFailed, "failed", true)
			r.alerter.Raised(ctx, inc, opened, "")
			r.alerter.Wait()
			tc.after(r, inc)
			r.hook.status.Store(http.StatusOK)
			if _, err := r.db.Exec(`UPDATE notification_deliveries SET updated_at = '2020-01-01T00:00:00.000Z'`); err != nil {
				t.Fatal(err)
			}
			router := notifyroute.NewRouter(r.db, notify.NewDispatcher(notify.NewChannelStore(r.db), nil, r.svc.Logger, r.db), nil, nil, r.svc.Logger)
			if _, sent := router.RecoverStuckDeliveries(ctx); sent != 0 {
				t.Fatalf("recovery sent %d", sent)
			}
			d := r.deliveries()
			if len(d) != 1 || d[0].Status != notifyroute.StatusFailed || d[0].Error != tc.want {
				t.Fatalf("deliveries %+v, want error %q", d, tc.want)
			}
		})
	}
}

// Delivery goes through the notification dispatcher, so its SSRF guard
// holds: a channel pointing at a loopback address is refused at connect
// time and the refusal is recorded.
func TestChannelAlerts_SSRFGuardHolds(t *testing.T) {
	ctx := context.Background()
	r := newAlertRig(t, false)
	r.route(r.channel.ID)
	inc, opened := r.raise(IncidentFailed, "failed", true)
	r.alerter.Raised(ctx, inc, opened, "")
	r.alerter.Wait()
	if n := len(r.hook.titles()); n != 0 {
		t.Fatalf("a loopback receiver was reached %d time(s)", n)
	}
	last := LastAlertDelivery(ctx, r.db, r.channel.ID)
	if last == nil || last.Status != "failed" || last.Error == nil {
		t.Fatalf("last delivery %+v", last)
	}
	res, err := r.alerter.Test(ctx, r.channel.ID)
	if err != nil || res.OK || res.Error == nil {
		t.Fatalf("test alert to loopback: %+v %v", res, err)
	}
}

// A channel that cannot be used gets a failed row saying why, and is not
// sent anything.
func TestChannelAlerts_UnusableChannelIsRecorded(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		break_ func(r *alertRig)
		want   string
	}{
		{"switched off", func(r *alertRig) {
			_, _ = r.db.Exec(`UPDATE notification_channels SET enabled = 0`)
		}, "the channel is switched off"},
		{"System health left out of its allowlist", func(r *alertRig) {
			_, _ = r.db.Exec(`UPDATE notification_channels SET categories_json = '["agents.approval"]'`)
		}, "the channel's category allowlist leaves out System health"},
		{"deleted", func(r *alertRig) {
			_, _ = r.db.Exec(`UPDATE notification_channels SET deleted_at = '2026-09-30'`)
		}, "the channel no longer exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newAlertRig(t, true)
			r.route(r.channel.ID)
			tc.break_(r)
			inc, opened := r.raise(IncidentFailed, "failed", true)
			r.alerter.Raised(ctx, inc, opened, "")
			r.alerter.Wait()
			d := r.deliveries()
			if len(d) != 1 || d[0].Status != notifyroute.StatusFailed || d[0].Error != tc.want || len(r.hook.titles()) != 0 {
				t.Fatalf("deliveries %+v posts %d, want failed %q", d, len(r.hook.titles()), tc.want)
			}
		})
	}
}

// fakeProvider records chat/push/incident sends (the shoutrrr seam).
type fakeProvider struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeProvider) Send(_ context.Context, url, message string, params map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, params["title"]+"\n"+message)
	return nil
}

// The picker lists the channels that can carry backup alerts; adding one to
// the route is validated against the same rules.
func TestAlertChannels_ListAndValidate(t *testing.T) {
	ctx := context.Background()
	r := newAlertRig(t, true)
	fp := &fakeProvider{}
	t.Cleanup(notify.SetProviderForTesting(fp))
	store := notify.NewChannelStore(r.db)
	mk := func(in notify.ChannelInput) string {
		t.Helper()
		ch, err := store.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return ch.ID
	}
	discord := mk(notify.ChannelInput{WorkspaceID: "ws_b", Type: notify.ChannelShoutrrr, Provider: notify.ProviderDiscord,
		Fields: map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/abc"}})
	personal := mk(notify.ChannelInput{WorkspaceID: "ws_a", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/me", Scope: notify.ScopeUser, OwnerUserID: "u1"})
	approvalsOnly := mk(notify.ChannelInput{WorkspaceID: "ws_a", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/a", Categories: []string{notify.CategoryAgentsApproval}})
	off := mk(notify.ChannelInput{WorkspaceID: "ws_a", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/off"})
	if _, err := r.db.Exec(`UPDATE notification_channels SET enabled = 0 WHERE id = ?`, off); err != nil {
		t.Fatal(err)
	}

	list, err := ListAlertChannels(ctx, r.db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AlertChannel{}
	for _, c := range list {
		got[c.ID] = c
	}
	if len(got) != 2 {
		t.Fatalf("listed %+v, want the webhook and Discord", list)
	}
	if c := got[r.channel.ID]; c.Kind != "webhook" || !strings.HasPrefix(c.Name, "Webhook 127.0.0.1:") || c.WorkspaceName != "Alpha" {
		t.Fatalf("webhook row %+v", c)
	}
	if c := got[discord]; c.Kind != "chat" || c.Name != "Discord · Beta" || c.Provider != notify.ProviderDiscord {
		t.Fatalf("discord row %+v", c)
	}

	for _, tc := range []struct {
		name         string
		next, before []string
		wantErr      string
	}{
		{"usable channels", []string{r.channel.ID, discord}, nil, ""},
		{"unknown", []string{"nch_nope"}, nil, "not a notification channel"},
		{"personal", []string{personal}, nil, "not a notification channel"},
		{"allowlist without System health", []string{approvalsOnly}, nil, "leaves out System health"},
		{"switched off", []string{off}, nil, "switched off"},
		{"already on the route is kept", []string{off}, []string{off}, ""},
	} {
		err := ValidateAlertChannels(ctx, r.db, tc.next, tc.before)
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !IsValidation(err) || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: %v, want %q", tc.name, err, tc.wantErr)
		}
	}

	// A provider switched off in Admin › Notifications leaves the picker,
	// and its sends fail with the reason.
	if _, err := r.db.Exec(`INSERT INTO app_settings (key, value) VALUES (?, 'false')`, notify.ProviderSettingKey(notify.ProviderDiscord)); err != nil {
		t.Fatal(err)
	}
	list, _ = ListAlertChannels(ctx, r.db)
	if len(list) != 1 {
		t.Fatalf("a switched-off provider is still offered: %+v", list)
	}
	if err := ValidateAlertChannels(ctx, r.db, []string{discord}, nil); err == nil || !strings.Contains(err.Error(), "Discord is switched off in Admin › Notifications") {
		t.Fatalf("validate: %v", err)
	}
	if _, err := r.db.Exec(`DELETE FROM app_settings WHERE key = ?`, notify.ProviderSettingKey(notify.ProviderDiscord)); err != nil {
		t.Fatal(err)
	}

	// Chat providers deliver through the same seam.
	r.route(discord)
	inc, opened := r.raise(IncidentOffsite, "no off-site copy", true)
	r.alerter.Raised(ctx, inc, opened, "")
	r.alerter.Wait()
	fp.mu.Lock()
	sent := append([]string(nil), fp.sent...)
	fp.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], AlertTitleRaised+"\n") || !strings.Contains(sent[0], "no off-site copy") || !strings.Contains(sent[0], AlertLink) {
		t.Fatalf("discord got %q", sent)
	}
}

func TestChannelAlerts_Test(t *testing.T) {
	ctx := context.Background()
	r := newAlertRig(t, true)
	res, err := r.alerter.Test(ctx, r.channel.ID)
	if err != nil || !res.OK || res.Error != nil || !strings.HasSuffix(res.Channel, " · Alpha") {
		t.Fatalf("test alert: %+v %v", res, err)
	}
	if got := r.hook.titles(); len(got) != 1 || got[0] != AlertTitleTest {
		t.Fatalf("receiver got %q", got)
	}
	if len(r.deliveries()) != 0 || LastAlertDelivery(ctx, r.db, r.channel.ID) != nil {
		t.Fatal("a test alert was recorded as a backup alert delivery")
	}
	r.hook.status.Store(http.StatusServiceUnavailable)
	if res, err := r.alerter.Test(ctx, r.channel.ID); err != nil || res.OK || res.Error == nil || !strings.Contains(*res.Error, "503") {
		t.Fatalf("failing test alert: %+v %v", res, err)
	}
	if _, err := r.alerter.Test(ctx, "nch_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
	if _, err := r.db.Exec(`UPDATE notification_channels SET enabled = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.alerter.Test(ctx, r.channel.ID); !IsValidation(err) {
		t.Fatalf("switched-off channel: %v", err)
	}
}
