package backupplan

// Backup alerts on the instance's notification channels.
//
// The inbox is where instance admins see an incident; it is also on the
// server that may be failing. Keys & alerts therefore lets an admin name
// notification channels — the ones already configured under a workspace's
// Settings › Notifications (Slack, Discord, Teams, e-mail, signed webhooks,
// Opsgenie and the rest of the provider catalog) — that hear about every
// incident as well. Nothing here stores a secret or dials a URL of its own:
//
//   - the channel row, its sealed service URL or signing secret, and its
//     decryption stay in internal/notify (ChannelStore.GetForDispatch);
//   - delivery is notify.Dispatcher.DeliverCategoryMessage, so the SSRF-safe
//     transport, the per-hop redirect check, the instance-wide provider kill
//     switch and the secret scrubber apply exactly as for any notification;
//   - every attempt is a notification_deliveries row (the outbox), whose
//     UNIQUE(channel_id, dedup_key) makes one message per incident state
//     change, and whose recovery sweep (internal/notifyroute) retries failed
//     and orphaned rows through AlertSourceKind's registered deriver.
//
// A channel is usable for backup alerts when it is workspace-wide (never a
// member's personal channel), switched on, its category allowlist admits
// System health, and — for a catalog provider — the provider is switched on
// in Admin › Notifications.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/notifyroute"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// AlertSourceKind is the outbox source_kind of a backup alert; source_id is
// the incident id and dedup_key "backup_incident:<incident>:<event>".
const AlertSourceKind = "backup_incident"

// AlertLink is where a backup alert sends its reader (made absolute at
// delivery against CREWSHIP_PUBLIC_URL, the instance's public base URL).
const AlertLink = "/admin/backups"

// Alert titles.
const (
	AlertTitleRaised   = "Backup needs attention"
	AlertTitleResolved = "Backup alert resolved"
	AlertTitleTest     = "Backup alert test"
)

// Alert events: what state change a message announces. A repeat carries the
// incident's count ("repeat:3"), so each repeat is its own message and a
// retried delivery of the same one is not.
const (
	AlertEventOpened   = "opened"
	AlertEventResolved = "resolved"
)

func alertEventRepeat(count int) string { return fmt.Sprintf("repeat:%d", count) }

func alertDedupKey(incidentID, event string) string {
	return AlertSourceKind + ":" + incidentID + ":" + event
}

// AlertChannel is one notification channel backup alerts can go to (the
// picker in Keys & alerts).
type AlertChannel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"` // chat | push | incident | email | webhook
	Provider      string `json:"provider"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	// LastDelivery is the newest backup alert sent to it; nil when none.
	LastDelivery *AlertDelivery `json:"last_delivery"`
}

// AlertDelivery is the outcome of one backup alert on one channel.
type AlertDelivery struct {
	Status     string  `json:"status"` // pending | sent | failed
	Error      *string `json:"error"`
	At         string  `json:"at"`
	IncidentID string  `json:"incident_id"`
}

// alertChannelRef is a channel id resolved to its workspace.
type alertChannelRef struct {
	ch            notify.Channel
	workspaceName string
}

// lookupAlertChannel resolves a channel id across workspaces (redacted: no
// secret). ErrNotFound for an unknown, deleted, personal channel or one in a
// deleted workspace — a personal channel is someone's own and is never
// offered, so it answers as absent rather than as "not allowed".
func lookupAlertChannel(ctx context.Context, db *sql.DB, id string) (*alertChannelRef, error) {
	var wsID, wsName string
	err := db.QueryRowContext(ctx, `SELECT c.workspace_id, w.name FROM notification_channels c
		JOIN workspaces w ON w.id = c.workspace_id
		WHERE c.id = ? AND c.deleted_at IS NULL AND w.deleted_at IS NULL`, id).Scan(&wsID, &wsName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ch, err := notify.NewChannelStore(db).Get(ctx, wsID, id)
	if errors.Is(err, notify.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if ch.Scope == notify.ScopeUser {
		return nil, ErrNotFound
	}
	return &alertChannelRef{ch: ch, workspaceName: wsName}, nil
}

// alertUnusable says why a channel cannot carry backup alerts, or "".
func alertUnusable(ctx context.Context, db *sql.DB, ch notify.Channel) string {
	if !ch.Enabled {
		return "the channel is switched off"
	}
	if !ch.AllowsCategory(notify.CategorySystemHealth) {
		return "the channel's category allowlist leaves out System health"
	}
	switch ch.Type {
	case notify.ChannelEmail, notify.ChannelWebhook:
	case notify.ChannelShoutrrr:
		spec, ok := notify.ProviderByName(ch.Provider)
		if !ok {
			return fmt.Sprintf("the provider %q is not supported on this server", ch.Provider)
		}
		on, err := notify.DefaultProviderGate(db)(ctx, ch.Provider)
		if err != nil {
			return "whether the provider is switched on cannot be read: " + err.Error()
		}
		if !on {
			return spec.Label + " is switched off in Admin › Notifications"
		}
	default:
		return fmt.Sprintf("the channel type %q cannot be delivered to", ch.Type)
	}
	return ""
}

// alertChannelView names a channel for the picker: what it is, where it
// points when that is not a secret, and whose it is.
func alertChannelView(ref *alertChannelRef) AlertChannel {
	ch := ref.ch
	out := AlertChannel{ID: ch.ID, Kind: string(ch.Type), WorkspaceID: ch.WorkspaceID, WorkspaceName: ref.workspaceName}
	var what string
	switch ch.Type {
	case notify.ChannelEmail:
		what = "Email " + ch.To
	case notify.ChannelWebhook:
		what = "Webhook"
		if u, err := url.Parse(ch.URL); err == nil && u.Host != "" {
			what += " " + u.Host
		}
	default:
		out.Provider = ch.Provider
		what = ch.Provider
		if spec, ok := notify.ProviderByName(ch.Provider); ok {
			what, out.Kind = spec.Label, string(spec.Category)
		}
	}
	out.Name = what + " · " + ref.workspaceName
	return out
}

// ListAlertChannels is every channel that can carry backup alerts now, with
// the outcome of the newest backup alert each one was sent.
func ListAlertChannels(ctx context.Context, db *sql.DB) ([]AlertChannel, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.id FROM notification_channels c JOIN workspaces w ON w.id = c.workspace_id
		WHERE c.deleted_at IS NULL AND w.deleted_at IS NULL AND c.enabled = 1 AND c.scope = 'workspace'
		ORDER BY w.name, c.created_at, c.id`)
	if err != nil {
		if strings.Contains(err.Error(), "no such") {
			return []AlertChannel{}, nil
		}
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []AlertChannel{}
	for _, id := range ids {
		ref, err := lookupAlertChannel(ctx, db, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if alertUnusable(ctx, db, ref.ch) != "" {
			continue
		}
		v := alertChannelView(ref)
		v.LastDelivery = LastAlertDelivery(ctx, db, id)
		out = append(out, v)
	}
	return out, nil
}

// ValidateAlertChannels checks the channels a settings change names. A
// channel already on the route (before) is kept even when it has since
// become unusable — its alerts then fail and say why, and refusing every
// other settings change until someone removes it would hide that — but a
// channel being added must exist and be usable now.
func ValidateAlertChannels(ctx context.Context, db *sql.DB, next, before []string) error {
	had := map[string]bool{}
	for _, id := range before {
		had[id] = true
	}
	for _, id := range next {
		if had[id] {
			continue
		}
		ref, err := lookupAlertChannel(ctx, db, id)
		if errors.Is(err, ErrNotFound) {
			return invalid("channels: %s is not a notification channel on this server (see GET …/backups/settings available_channels)", id)
		}
		if err != nil {
			return err
		}
		if why := alertUnusable(ctx, db, ref.ch); why != "" {
			return invalid("channels: %s cannot carry backup alerts: %s", alertChannelView(ref).Name, why)
		}
	}
	return nil
}

// LastAlertDelivery is the newest backup alert (not a test) on a channel.
func LastAlertDelivery(ctx context.Context, db *sql.DB, channelID string) *AlertDelivery {
	var d AlertDelivery
	var errText sql.NullString
	err := db.QueryRowContext(ctx, `SELECT status, error, updated_at, COALESCE(source_id,'') FROM notification_deliveries
		WHERE channel_id = ? AND source_kind = ? AND status IN ('pending','sent','failed')
		ORDER BY updated_at DESC, created_at DESC LIMIT 1`, channelID, AlertSourceKind).Scan(&d.Status, &errText, &d.At, &d.IncidentID)
	if err != nil {
		return nil
	}
	d.Error = strPtr(errText)
	return &d
}

// AlertDeliveryFailures is one Overview needs-attention item per channel on
// the route whose newest backup alert could not be delivered.
func AlertDeliveryFailures(ctx context.Context, db *sql.DB) []AttentionItem {
	set, err := LoadSettings(ctx, db)
	if err != nil {
		return nil
	}
	var out []AttentionItem
	for _, id := range set.Channels {
		last := LastAlertDelivery(ctx, db, id)
		if last == nil || last.Status != "failed" {
			continue
		}
		name := id
		if ref, err := lookupAlertChannel(ctx, db, id); err == nil {
			name = alertChannelView(ref).Name
		}
		detail := "no reason recorded"
		if last.Error != nil && *last.Error != "" {
			detail = *last.Error
		}
		out = append(out, AttentionItem{ID: "alerts:" + id, Severity: "warn",
			Title:  "Backup alerts could not be delivered to " + name,
			Detail: detail, Action: &AttentionAction{Kind: "keys", Label: "Keys & alerts"}})
	}
	return out
}

// ── The message ─────────────────────────────────────────────────────────────

// alertPlanLabel names what an incident is about.
func alertPlanLabel(ctx context.Context, db *sql.DB, inc *Incident) string {
	switch {
	case inc.PlanID != nil:
		if p, err := GetPlan(ctx, db, *inc.PlanID); err == nil {
			return p.Name
		}
		return *inc.PlanID
	case inc.Kind == IncidentDrill:
		return "Test restores"
	}
	return "Manual backups"
}

// alertLastGood is how long ago the incident's plan (or, for an incident of
// no plan, any backup) last succeeded.
func alertLastGood(ctx context.Context, db *sql.DB, inc *Incident, now time.Time) string {
	if inc.PlanID != nil {
		return Ago(lastGoodRun(ctx, db, *inc.PlanID), now)
	}
	var ended sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MAX(ended_at) FROM backup_runs WHERE status IN ('done','incomplete')`).Scan(&ended); err != nil || !ended.Valid {
		return Ago(nil, now)
	}
	return Ago(optTime(ended), now)
}

// alertDetailCap bounds the failure detail carried in a message.
const alertDetailCap = 500

// AlertMessage composes the message a channel receives for an incident
// state change (AlertEventOpened, a repeat, AlertEventResolved): the plan,
// what happened, how often, how long ago the last good backup was, and a
// link to Backups › Overview. detail is the failure's own text (a run
// error), optional.
func AlertMessage(ctx context.Context, db *sql.DB, inc *Incident, event, detail string, now time.Time) notify.CategoryMessage {
	plan := alertPlanLabel(ctx, db, inc)
	last := alertLastGood(ctx, db, inc, now)
	var b strings.Builder
	title, priority := AlertTitleRaised, "high"
	if inc.Kind == IncidentIncomplete || inc.Kind == IncidentStale {
		priority = "medium"
	}
	fmt.Fprintf(&b, "Plan: %s\n", plan)
	if event == AlertEventResolved {
		title, priority = AlertTitleResolved, "low"
		fmt.Fprintf(&b, "Resolved: %s\n", inc.Message)
		fmt.Fprintf(&b, "It happened %s since %s.\n", plural(inc.Count, "time", "times"), inc.FirstAt)
	} else {
		fmt.Fprintf(&b, "What happened: %s\n", inc.Message)
		if d := strings.TrimSpace(detail); d != "" {
			if len(d) > alertDetailCap {
				d = d[:alertDetailCap] + "…"
			}
			fmt.Fprintf(&b, "Detail: %s\n", d)
		}
		if inc.Count > 1 {
			fmt.Fprintf(&b, "Repeats: %s since %s · one incident\n", plural(inc.Count, "time", "times"), inc.FirstAt)
		}
	}
	fmt.Fprintf(&b, "Last good backup: %s", last)
	vars := map[string]any{
		"incident_id": inc.ID, "incident_kind": inc.Kind, "event": event, "plan": plan,
		"count": inc.Count, "first_at": inc.FirstAt, "last_good_backup": last,
	}
	if inc.PlanID != nil {
		vars["plan_id"] = *inc.PlanID
	}
	if inc.RunID != nil {
		vars["run_id"] = *inc.RunID
	}
	return notify.CategoryMessage{
		Category: notify.CategorySystemHealth, Title: title, Body: b.String(), Priority: priority,
		SourceKind: AlertSourceKind, SourceID: inc.ID,
		Links: []notify.Link{{Label: "Open Backups", Path: AlertLink}}, Vars: vars,
	}
}

// runError is the recorded error of a run ("" when none).
func runError(ctx context.Context, db *sql.DB, runID *string) string {
	if runID == nil || *runID == "" {
		return ""
	}
	var e sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT error FROM backup_runs WHERE id = ?`, *runID).Scan(&e)
	return e.String
}

// deriveAlert is the recovery sweep's SourceDeriver for AlertSourceKind: it
// rebuilds the message from the incident as it stands, and gives the row up
// (notifyroute.SourceGone) when the incident is gone, an alert of an open incident
// would arrive after it was resolved, or the channel is no longer on the
// route or usable.
func deriveAlert(ctx context.Context, db *sql.DB, d notifyroute.Delivery) (notify.CategoryMessage, error) {
	event := strings.TrimPrefix(d.DedupKey, AlertSourceKind+":"+d.SourceID+":")
	inc, err := GetIncident(ctx, db, d.SourceID)
	if errors.Is(err, ErrNotFound) {
		return notify.CategoryMessage{}, notifyroute.SourceGone("the backup incident no longer exists")
	}
	if err != nil {
		return notify.CategoryMessage{}, err
	}
	if event != AlertEventResolved && inc.State == "resolved" {
		return notify.CategoryMessage{}, notifyroute.SourceGone("the backup incident was resolved before this alert went out")
	}
	set, err := LoadSettings(ctx, db)
	if err != nil {
		return notify.CategoryMessage{}, err
	}
	onRoute := false
	for _, id := range set.Channels {
		onRoute = onRoute || id == d.ChannelID
	}
	if !onRoute {
		return notify.CategoryMessage{}, notifyroute.SourceGone("the channel is no longer on the backup alert route")
	}
	ref, err := lookupAlertChannel(ctx, db, d.ChannelID)
	if errors.Is(err, ErrNotFound) {
		return notify.CategoryMessage{}, notifyroute.SourceGone("the channel no longer exists")
	}
	if err != nil {
		return notify.CategoryMessage{}, err
	}
	if why := alertUnusable(ctx, db, ref.ch); why != "" {
		return notify.CategoryMessage{}, notifyroute.SourceGone(why)
	}
	msg := AlertMessage(ctx, db, inc, event, runError(ctx, db, inc.RunID), time.Now())
	msg.WorkspaceID = d.WorkspaceID
	return msg, nil
}

// ── Delivery ────────────────────────────────────────────────────────────────

// ChannelAlerter delivers backup incidents to the channels on the route
// (backup_settings.channels). internal/api's alerter calls it next to the
// inbox; it never blocks the run that raised the incident: the outbox row is
// written inline (so a crash leaves it for the recovery sweep) and the send
// runs in the background.
type ChannelAlerter struct {
	DB         *sql.DB
	Dispatcher *notify.Dispatcher
	Logger     *slog.Logger
	// Begin registers a background delivery with the caller's drain
	// (internal/api's beginBackgroundWork) and returns its done func; nil
	// registers nothing.
	Begin func() func()
	// Now is the clock (tests); nil is time.Now.
	Now func() time.Time

	wg sync.WaitGroup
}

// NewChannelAlerter wires the alerter and registers its recovery deriver
// with the notification outbox.
func NewChannelAlerter(db *sql.DB, d *notify.Dispatcher, logger *slog.Logger) *ChannelAlerter {
	if logger == nil {
		logger = slog.Default()
	}
	notifyroute.RegisterSourceDeriver(AlertSourceKind, deriveAlert)
	return &ChannelAlerter{DB: db, Dispatcher: d, Logger: logger}
}

func (c *ChannelAlerter) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Wait blocks until every background delivery has finished (tests).
func (c *ChannelAlerter) Wait() { c.wg.Wait() }

// Raised sends the incident's opening (or its repeat) to every channel on
// the route.
func (c *ChannelAlerter) Raised(ctx context.Context, inc *Incident, opened bool, detail string) {
	if c == nil || inc == nil {
		return
	}
	set, err := LoadSettings(ctx, c.DB)
	if err != nil || len(set.Channels) == 0 {
		return
	}
	event := AlertEventOpened
	if !opened {
		event = alertEventRepeat(inc.Count)
	}
	c.fanOut(ctx, set.Channels, inc, event, AlertMessage(ctx, c.DB, inc, event, detail, c.now()))
}

// Resolved sends "resolved" to the channels on the route that were told the
// incident was open — an incident announced nowhere (its kind switched off,
// or opened before the channel was added) resolves quietly.
func (c *ChannelAlerter) Resolved(ctx context.Context, inc *Incident) {
	if c == nil || inc == nil {
		return
	}
	set, err := LoadSettings(ctx, c.DB)
	if err != nil || len(set.Channels) == 0 {
		return
	}
	var told []string
	for _, id := range set.Channels {
		var n int
		_ = c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND source_kind = ?
			AND source_id = ? AND dedup_key <> ?`, id, AlertSourceKind, inc.ID, alertDedupKey(inc.ID, AlertEventResolved)).Scan(&n)
		if n > 0 {
			told = append(told, id)
		}
	}
	if len(told) == 0 {
		return
	}
	c.fanOut(ctx, told, inc, AlertEventResolved, AlertMessage(ctx, c.DB, inc, AlertEventResolved, "", c.now()))
}

// fanOut writes one outbox row per channel for this state change and sends
// the new ones. A row that already exists (the same change raised twice) is
// not sent again; a channel that cannot be used gets a failed row saying why.
func (c *ChannelAlerter) fanOut(ctx context.Context, channels []string, inc *Incident, event string, msg notify.CategoryMessage) {
	store := notifyroute.NewDeliveryStore(c.DB)
	for _, id := range channels {
		ref, lookupErr := lookupAlertChannel(ctx, c.DB, id)
		wsID := ""
		if ref != nil {
			wsID = ref.ch.WorkspaceID
		}
		rowID, created, err := store.InsertPending(ctx, notifyroute.Delivery{
			WorkspaceID: wsID, ChannelID: id, Category: notify.CategorySystemHealth,
			DedupKey: alertDedupKey(inc.ID, event), SourceKind: AlertSourceKind, SourceID: inc.ID, Title: msg.Title,
		})
		if err != nil {
			c.Logger.Warn("backup alert: write delivery", "channel", id, "incident", inc.ID, "error", err)
			continue
		}
		if !created {
			continue // this state change was already sent (or is being sent) here
		}
		why := ""
		switch {
		case errors.Is(lookupErr, ErrNotFound):
			why = "the channel no longer exists"
		case lookupErr != nil:
			why = "the channel cannot be read: " + lookupErr.Error()
		default:
			why = alertUnusable(ctx, c.DB, ref.ch)
		}
		if why != "" {
			c.fail(ctx, store, rowID, id, inc.ID, why)
			continue
		}
		ch, err := notify.NewChannelStore(c.DB).GetForDispatch(ctx, wsID, id)
		if err != nil {
			c.fail(ctx, store, rowID, id, inc.ID, "the channel cannot be read: "+err.Error())
			continue
		}
		m := msg
		m.WorkspaceID = wsID
		c.spawn(func() { c.send(store, rowID, ch, m, inc.ID) })
	}
}

func (c *ChannelAlerter) fail(ctx context.Context, store *notifyroute.DeliveryStore, rowID, channelID, incidentID, why string) {
	c.Logger.Warn("backup alert not delivered", "channel", channelID, "incident", incidentID, "error", why)
	if err := store.MarkFailed(ctx, rowID, why); err != nil {
		c.Logger.Warn("backup alert: record failure", "channel", channelID, "error", err)
	}
}

func (c *ChannelAlerter) spawn(fn func()) {
	done := func() {}
	if c.Begin != nil {
		done = c.Begin()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer done()
		fn()
	}()
}

// alertSendTimeout bounds one delivery attempt.
const alertSendTimeout = 30 * time.Second

// send delivers one row and records the outcome. While a backup's quiet
// window is closing the row stays pending, and the recovery sweep sends it
// once the window is over.
func (c *ChannelAlerter) send(store *notifyroute.DeliveryStore, rowID string, ch notify.Channel, msg notify.CategoryMessage, incidentID string) {
	ctx, cancel := context.WithTimeout(context.Background(), alertSendTimeout)
	defer cancel()
	wr, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	defer wr.Leave()
	if err := c.dispatcher().DeliverCategoryMessage(ctx, ch, msg); err != nil {
		c.fail(ctx, store, rowID, ch.ID, incidentID, err.Error())
		return
	}
	if err := store.MarkSent(ctx, rowID); err != nil {
		c.Logger.Warn("backup alert: record delivery", "channel", ch.ID, "error", err)
	}
}

func (c *ChannelAlerter) dispatcher() *notify.Dispatcher {
	if c.Dispatcher != nil {
		return c.Dispatcher
	}
	return notify.NewDispatcher(notify.NewChannelStore(c.DB), nil, c.Logger, c.DB)
}

// AlertTestResult is the outcome of a test alert.
type AlertTestResult struct {
	OK        bool    `json:"ok"`
	ChannelID string  `json:"channel_id"`
	Channel   string  `json:"channel"`
	Error     *string `json:"error"`
	SentAt    string  `json:"sent_at"`
}

// Test sends a test alert to one channel now and says whether it arrived.
// The channel need not be on the route yet (the picker tests before saving)
// but must be usable: ErrNotFound for an unknown channel, a validation error
// for an unusable one. Tests are not written to the outbox, so they neither
// retry nor count as a backup alert's delivery.
func (c *ChannelAlerter) Test(ctx context.Context, channelID string) (*AlertTestResult, error) {
	ref, err := lookupAlertChannel(ctx, c.DB, channelID)
	if err != nil {
		return nil, err
	}
	view := alertChannelView(ref)
	if why := alertUnusable(ctx, c.DB, ref.ch); why != "" {
		return nil, invalid("%s cannot carry backup alerts: %s", view.Name, why)
	}
	ch, err := notify.NewChannelStore(c.DB).GetForDispatch(ctx, ref.ch.WorkspaceID, channelID)
	if err != nil {
		return nil, err
	}
	now := c.now()
	msg := notify.CategoryMessage{
		WorkspaceID: ch.WorkspaceID, Category: notify.CategorySystemHealth, Title: AlertTitleTest, Priority: "low",
		Body: "This is a test of Crewship backup alerts. When a backup fails, is incomplete, goes stale, " +
			"misses its off-site copy or fails a drill, the alert arrives here and in the instance admins' inbox.",
		SourceKind: AlertSourceKind + "_test", SourceID: channelID,
		Links: []notify.Link{{Label: "Open Backups", Path: AlertLink}},
	}
	tctx, cancel := context.WithTimeout(ctx, alertSendTimeout)
	defer cancel()
	out := &AlertTestResult{OK: true, ChannelID: channelID, Channel: view.Name, SentAt: ts(now)}
	if err := c.dispatcher().DeliverCategoryMessage(tctx, ch, msg); err != nil {
		e := err.Error()
		out.OK, out.Error = false, &e
	}
	return out, nil
}
