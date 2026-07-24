package notifyroute

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/inbox"
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
	dispatcher *notify.Dispatcher
	presence   PresenceChecker // nil = no presence gate (never suppress)
	limiter    *RateLimiter    // nil = no rate limiting
	logger     *slog.Logger
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
	category := notify.CategoryForKind(item.Kind)
	if category == "" {
		return // this inbox kind has no external-notification mapping (yet)
	}
	// Detach from the request/step context (which may be cancelled the
	// instant the caller returns) but keep it as a value-source is
	// unnecessary here — a fresh background context is correct: delivery
	// must outlive the triggering request.
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

// deliverToChannel runs the rate gate, writes the outbox row (coalescing
// on (channel_id, dedup_key)), attempts delivery, and updates the log.
func (r *Router) deliverToChannel(ctx context.Context, category string, item inbox.Item, uid string, ch notify.Channel, dedupKey string) {
	d := Delivery{
		WorkspaceID: item.WorkspaceID,
		ChannelID:   ch.ID,
		UserID:      uid,
		Category:    category,
		DedupKey:    dedupKey,
		SourceKind:  item.Kind,
		SourceID:    item.SourceID,
		Title:       item.Title,
	}

	if !notify.BypassesRateGate(category) && r.limiter != nil && !r.limiter.Allow(uid, ch.ID, category) {
		if err := r.deliveries.InsertDropped(ctx, d, StatusDroppedRate); err != nil {
			r.logger.Warn("notifyroute: log dropped_rate", "error", err)
		}
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

	msg := notify.CategoryMessage{
		WorkspaceID: item.WorkspaceID,
		Category:    category,
		Title:       item.Title,
		Body:        item.BodyMD,
		Priority:    item.Priority,
		SourceKind:  item.Kind,
		SourceID:    item.SourceID,
	}
	if url, ok := item.Payload["chat_url"].(string); ok {
		msg.URL = url
	}

	if err := r.dispatcher.DeliverCategoryMessage(ctx, ch, msg); err != nil {
		if merr := r.deliveries.MarkFailed(ctx, id, err.Error()); merr != nil {
			r.logger.Warn("notifyroute: mark delivery failed", "error", merr)
		}
		r.logger.Warn("notifyroute: delivery failed", "error", err, "channel_id", ch.ID, "category", category)
		return
	}
	if err := r.deliveries.MarkSent(ctx, id); err != nil {
		r.logger.Warn("notifyroute: mark delivery sent", "error", err)
	}
}
