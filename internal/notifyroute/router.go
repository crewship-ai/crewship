package notifyroute

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

// PresenceChecker reports whether a user currently has a live subscription
// on a channel — the same seam internal/chatnotify.Hub exposes
// (IsUserSubscribed), duck-typed here so this package doesn't need to
// import chatnotify just for one method. Production wires *ws.Hub, which
// already satisfies this shape.
type PresenceChecker interface {
	IsUserSubscribed(channel, userID string) bool
}

// Router implements inbox.ExternalNotifier: it is the concrete fan-out
// wired at boot via inbox.SetExternalNotifier (see cmd_start.go). See the
// package doc for the full pipeline a call runs through.
type Router struct {
	db         *sql.DB
	channels   *notify.ChannelStore
	prefs      *PrefStore
	deliveries *DeliveryStore
	templates  *notify.TemplateStore
	dispatcher *notify.Dispatcher
	presence   PresenceChecker // nil = no presence gate (never suppress)
	limiter    *RateLimiter    // nil = no rate limiting
	logger     *slog.Logger
	// journal records outbound delivery attempts so they show up on the
	// Activity timeline. nil = deliveries stay invisible there (test rigs).
	// See journal_bridge.go.
	journal journalEmitter
}

// NewRouter wires a Router. presence and limiter may be nil (both degrade
// safely: nil presence never suppresses for "watching live," nil limiter
// never rate-drops).
func NewRouter(db *sql.DB, dispatcher *notify.Dispatcher, presence PresenceChecker, limiter *RateLimiter, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{
		db:         db,
		channels:   notify.NewChannelStore(db),
		prefs:      NewPrefStore(db),
		deliveries: NewDeliveryStore(db),
		templates:  notify.NewTemplateStore(db),
		dispatcher: dispatcher,
		presence:   presence,
		limiter:    limiter,
		logger:     logger,
	}
}

// Compile-time check: Router satisfies inbox.ExternalNotifier.
var _ inbox.ExternalNotifier = (*Router)(nil)

// NotifyInboxItem implements inbox.ExternalNotifier. It is fire-and-forget
// by contract (see that interface's doc comment): it spawns its own
// goroutine so the inbox write-through call site (an HTTP handler, a
// pipeline step) is never slowed down by network delivery, with panic
// recovery mirroring cmd_start.go's #850 terminal-notifier wiring — a
// delivery-path bug must never take the writer down with it.
func (r *Router) NotifyInboxItem(ctx context.Context, item inbox.Item) {
	if r == nil || r.db == nil {
		return
	}
	category := categoryForItem(item)
	if category == "" {
		return // this inbox kind has no external-notification mapping (yet)
	}
	r.notifyItem(ctx, category, item)
}

// categoryForItem resolves the notification category an inbox item routes
// under: what its producer declared, else what its kind maps to.
//
// The kind mapping alone could not serve every producer. A routine's notify
// step writes kind "message" whatever it is reporting, so a failure, a digest
// and a deploy result all arrived as chat.replies and the preference matrix
// people tune was invisible to the author who knew what the event was.
//
// A declared category that is not real falls back rather than being trusted.
// inbox.Item is a leaf type that cannot import the category vocabulary to
// validate itself, and routing into a category nothing matches would deliver
// to nobody while every log line reported success — the expensive failure.
func categoryForItem(item inbox.Item) string {
	if item.Category != "" && notify.ValidCategory(item.Category) {
		return item.Category
	}
	return notify.CategoryForKind(item.Kind)
}

// notifyItem is the shared fan-out entry point for BOTH producers — the
// inbox write-through (NotifyInboxItem) and the journal commit observer
// (ObserveJournal). Having one funnel is the point: the presence gate,
// preference matrix, allowlist, priority floor, rate gate and delivery log
// are applied in exactly one place, so a change to any of them can never
// apply to one producer and silently miss the other.
//
// Detaches onto its own goroutine with a fresh background context — both
// call sites are on a hot path (an HTTP handler, the journal write path) and
// delivery must outlive the request that triggered it. Panic recovery
// mirrors cmd_start.go's terminal-notifier wiring: a delivery-path bug must
// never take the caller down with it.
func (r *Router) notifyItem(ctx context.Context, category string, item inbox.Item) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Error("notifyroute: panic in fan-out goroutine", "panic", p, "kind", item.Kind, "source_id", item.SourceID)
			}
		}()
		r.route(context.Background(), category, item)
	}()
}

// route resolves the audience, applies presence + preferences + rate
// limiting per recipient/channel, and delivers. Best-effort throughout:
// every failure is logged and skipped, never propagated — there is no
// caller left to propagate to once NotifyInboxItem's goroutine started.
func (r *Router) route(ctx context.Context, category string, item inbox.Item) {
	recipients, err := r.resolveAudience(ctx, item)
	if err != nil {
		r.logger.Warn("notifyroute: resolve audience", "error", err, "kind", item.Kind, "source_id", item.SourceID)
		return
	}
	for _, uid := range recipients {
		if r.presence != nil && item.Kind == inbox.KindMessage {
			if chatID, ok := item.Payload["chat_id"].(string); ok && chatID != "" {
				if r.presence.IsUserSubscribed("session:"+chatID, uid) {
					continue // watching live — no external push needed
				}
			}
		}
		r.routeToUser(ctx, category, item, uid)
	}
}

// resolveAudience mirrors internal/pipeline's resolveNotifyTargets output
// shape (user / role / workspace-wide) but reads the ALREADY-WRITTEN
// inbox item's target fields rather than a notify-step `to` selector — by
// the time NotifyInboxItem fires, the writer has already decided who the
// in-product inbox row is for.
func (r *Router) resolveAudience(ctx context.Context, item inbox.Item) ([]string, error) {
	if item.TargetUserID != "" {
		return []string{item.TargetUserID}, nil
	}
	if item.TargetRole != "" {
		rows, err := r.db.QueryContext(ctx,
			`SELECT user_id FROM workspace_members WHERE workspace_id = ? AND role = ?`,
			item.WorkspaceID, item.TargetRole)
		if err != nil {
			return nil, fmt.Errorf("query role members: %w", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var uid string
			if err := rows.Scan(&uid); err == nil {
				out = append(out, uid)
			}
		}
		return out, rows.Err()
	}
	// Neither set: workspace-wide broadcast (e.g. a memory-consolidation
	// notice with no single owner).
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id FROM workspace_members WHERE workspace_id = ?`, item.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("query workspace members: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil {
			out = append(out, uid)
		}
	}
	return out, rows.Err()
}

// routeToUser evaluates every channel available to uid against their
// preference matrix, the admin per-channel category allowlist, the
// priority floor, and the anti-storm rate gate, then delivers to each
// channel that clears all four.
func (r *Router) routeToUser(ctx context.Context, category string, item inbox.Item, uid string) {
	channels, err := r.channels.ListForUser(ctx, item.WorkspaceID, uid)
	if err != nil || len(channels) == 0 {
		if err != nil {
			r.logger.Warn("notifyroute: list channels", "error", err, "user_id", uid)
		}
		return
	}
	cells, err := r.prefs.Get(ctx, item.WorkspaceID, uid)
	if err != nil {
		r.logger.Warn("notifyroute: get prefs", "error", err, "user_id", uid)
		return
	}
	idx := indexCells(cells)
	dedupKey := category + ":" + item.SourceID

	for _, ch := range channels {
		if idx.state(category, ch.ID) != "immediate" {
			// off (the default) — the user never opted this cell in. Not
			// logged: with N channels x 9 categories this is overwhelmingly
			// the common case, and "never subscribed" isn't an auditable
			// drop the way an explicit block on an OPTED-IN cell is.
			continue
		}
		d := Delivery{
			WorkspaceID: item.WorkspaceID, ChannelID: ch.ID, UserID: uid,
			Category: category, DedupKey: dedupKey,
			SourceKind: item.Kind, SourceID: item.SourceID, Title: item.Title,
		}
		switch {
		case !ch.AllowsCategory(category):
			// The user opted in, but the admin's per-channel category
			// allowlist excludes it — worth an auditable dropped_pref row
			// since it reads as "why didn't my notification arrive?" from
			// the user's side.
			if err := r.deliveries.InsertDropped(ctx, d, StatusDroppedPref); err != nil {
				r.logger.Warn("notifyroute: log dropped_pref (admin allowlist)", "error", err)
			}
		case idx.muted(ch.ID):
			if err := r.deliveries.InsertDropped(ctx, d, StatusDroppedPref); err != nil {
				r.logger.Warn("notifyroute: log dropped_pref (channel muted)", "error", err)
			}
		case notify.PriorityRank(item.Priority) < notify.PriorityRank(ch.MinPriority):
			if err := r.deliveries.InsertDropped(ctx, d, StatusDroppedPref); err != nil {
				r.logger.Warn("notifyroute: log dropped_pref (priority floor)", "error", err)
			}
		default:
			r.deliverToChannel(ctx, category, item, uid, ch, dedupKey)
		}
	}
}

// applyTemplate rewrites a message's wording from the operator's template for
// its category, when one exists.
//
// Only for notifications the PRODUCT computes. A message that arrives as inbox
// kind "message" was written by somebody — a routine author's notify step, an
// agent's chat reply — and an operator's category template has no business
// overwriting another person's words. That would be a different feature, and a
// worse one, wearing this one's name.
//
// Applied BEFORE delivery so the envelope scrub still covers whatever the
// template pulled in: a template can reference any fact, including one holding
// a secret, and it must not become a way around redaction.
//
// A template that cannot be read is not a reason to lose the notification. The
// producer's own wording is a correct message; failing the delivery over a
// missing override would trade a cosmetic problem for a silent one.
func (r *Router) applyTemplate(ctx context.Context, msg notify.CategoryMessage, channelID string) notify.CategoryMessage {
	if r.templates == nil || msg.SourceKind == inbox.KindMessage {
		return msg
	}
	tmpl, err := r.templates.Resolve(ctx, msg.WorkspaceID, msg.Category, channelID)
	if err != nil {
		r.logger.Warn("notifyroute: resolve message template",
			"error", err, "category", msg.Category, "channel_id", channelID)
		return msg
	}
	return tmpl.Apply(msg)
}

// deliverToChannel runs the rate gate, writes the outbox row (coalescing
// on (channel_id, dedup_key)), attempts delivery, and updates the log.
func (r *Router) deliverToChannel(ctx context.Context, category string, item inbox.Item, uid string, ch notify.Channel, dedupKey string) {
	// The message is composed FIRST, template included, so everything that
	// records a title records the one the recipient will actually see. The
	// outbox row and the Activity timeline used to capture the producer's
	// title, taken before the template ran — leaving an operator asking "why
	// did that message say something else?" reading a log showing the wording
	// they did not receive, which is the one question this log exists to
	// answer.
	links, vars := notificationFacts(item.Kind, item.Payload)
	msg := notify.CategoryMessage{
		WorkspaceID: item.WorkspaceID,
		Category:    category,
		Title:       item.Title,
		Body:        item.BodyMD,
		Priority:    item.Priority,
		SourceKind:  item.Kind,
		SourceID:    item.SourceID,
		Links:       links,
		Vars:        vars,
	}
	msg = r.applyTemplate(ctx, msg, ch.ID)

	d := Delivery{
		WorkspaceID: item.WorkspaceID,
		ChannelID:   ch.ID,
		UserID:      uid,
		Category:    category,
		DedupKey:    dedupKey,
		SourceKind:  item.Kind,
		SourceID:    item.SourceID,
		Title:       msg.Title,
	}

	if !notify.BypassesRateGate(category) && r.limiter != nil && !r.limiter.Allow(uid, ch.ID, category) {
		if err := r.deliveries.InsertDropped(ctx, d, StatusDroppedRate); err != nil {
			r.logger.Warn("notifyroute: log dropped_rate", "error", err)
		}
		r.emitDeliveryJournal(ctx, journal.EntryNotificationDropped, journal.SeverityNotice,
			ch, category, msg.Title, "rate limit")
		return
	}

	id, created, err := r.deliveries.InsertPending(ctx, d)
	if err != nil {
		r.logger.Warn("notifyroute: insert pending delivery", "error", err)
		return
	}
	if !created {
		return // coalesced: an identical (channel, dedup_key) delivery already exists
	}

	if err := r.dispatcher.DeliverCategoryMessage(ctx, ch, msg); err != nil {
		if merr := r.deliveries.MarkFailed(ctx, id, err.Error()); merr != nil {
			r.logger.Warn("notifyroute: mark delivery failed", "error", merr)
		}
		r.logger.Warn("notifyroute: delivery failed", "error", err, "channel_id", ch.ID, "category", category)
		r.emitDeliveryJournal(ctx, journal.EntryNotificationFailed, journal.SeverityError,
			ch, category, msg.Title, err.Error())
		return
	}
	if err := r.deliveries.MarkSent(ctx, id); err != nil {
		r.logger.Warn("notifyroute: mark delivery sent", "error", err)
	}
	r.emitDeliveryJournal(ctx, journal.EntryNotificationDelivered, journal.SeverityInfo,
		ch, category, msg.Title, "")
}
