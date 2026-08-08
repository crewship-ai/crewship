package notifyroute

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

// The journal is the OBSERVATIONAL producer of notification categories; the
// inbox is the ACTIONABLE one (see internal/notify/categories.go).
//
// Why two producers rather than more inbox kinds: the inbox is a human-
// attention queue where every row is a card someone is meant to read and
// often resolve. That is the right model for an approval and the wrong one
// for "budget crossed its limit" or "an issue moved to In Review" — nobody
// wants forty inbox cards a day, but they may well want those in Slack. The
// journal already records all of it and had no consumer.
//
// Everything downstream (preference matrix, admin allowlist, priority floor,
// rate gate, delivery log) keys off the category and does not care which
// producer minted it.

// journalCategories maps a journal entry type to the notification category it
// fans out to. Absence means "never notify" — the default, and the reason
// this is an allowlist rather than a denylist.
//
// The selection is deliberately conservative. Most journal types are
// telemetry (llm.call, exec.command, cost.incurred, container.metrics) or
// fire per-request (network.egress runs on EVERY outbound call), and routing
// those would turn a chat channel into a firehose that people mute — at
// which point they miss the approval request too.
var journalCategories = map[journal.EntryType]string{
	// Routines. pipeline.run.failed is deliberately absent: the scheduler
	// already writes a `failed_run` inbox item for it, and mapping both
	// would deliver the same failure twice through different producers.
	journal.EntryPipelineRunCompleted: notify.CategoryRoutinesCompleted,
	journal.EntryPipelineStepSkipped:  notify.CategoryRoutinesSkipped,

	// Issues.
	journal.EntryMissionCreated:  notify.CategoryIssuesCreated,
	journal.EntryMissionStatus:   notify.CategoryIssuesState,
	journal.EntryMissionAssigned: notify.CategoryIssuesAssigned,
	journal.EntryMissionComment:  notify.CategoryIssuesComment,
	// agent.mentioned had a producer as of #1768 item 3 (the @mention trigger)
	// and no category, which meant a mention journalled and never notified —
	// the exact "audited but unnotifiable" gap that PR's F1 exists to close.
	//
	// It routes to issues.comment rather than to a fifteenth category, and that
	// is a decision, not a shortcut:
	//
	//   * a mention ONLY ever arrives inside an issue comment. There is no
	//     other producer and no plan for one, so a dedicated toggle would
	//     always be a strictly narrower duplicate of the one next to it;
	//   * the user intent the matrix models is "tell me when there is talk on
	//     my issues". Someone who muted issue comments and then received a
	//     mention would read that as the mute being broken;
	//   * the taxonomy is closed on purpose. Its last change (v169 /
	//     notify_taxonomy) had to REWRITE every stored user_notification_prefs
	//     cell and every notification_channels allowlist to keep opted-in users
	//     opted in. That cost is worth paying for a category with a real,
	//     distinct producer; it is not worth paying to split one producer in
	//     two, and a new row would default to whatever the migration chose
	//     rather than to what each user actually wanted.
	//
	// If mentions ever get a producer OUTSIDE a comment — a mention in an issue
	// description, or in chat — the calculus changes and a real category with a
	// real preference migration becomes the right answer.
	journal.EntryAgentMentioned: notify.CategoryIssuesComment,

	// Agents. keeper.request is absent — it already becomes an inbox
	// escalation, so mapping it here would double-deliver.
	journal.EntryAgentError:              notify.CategoryAgentsError,
	journal.EntryProvisioningFailed:      notify.CategoryAgentsError,
	journal.EntryProvisioningBuildFailed: notify.CategoryAgentsError,
	journal.EntrySidecarStale:            notify.CategoryAgentsError,
	journal.EntryBudgetExceed:            notify.CategoryAgentsBudget,
	journal.EntryBudgetWarning:           notify.CategoryAgentsBudget,

	// System + security.
	//
	// image.stale is system.health rather than a fifteenth category, and rather
	// than joining sidecar.stale on agents.error. Both halves are decisions
	// (#1845):
	//
	// Not a new category, because the taxonomy is closed on purpose — see the
	// agent.mentioned note above, and note additionally that the CHECK on
	// user_notification_prefs.category is generated from notify.AllCategories
	// at migration v169, so a fifteenth row is unstorable on every instance
	// that has already migrated until a widening migration lands. That cost is
	// worth paying for a producer with no home; this one has one.
	//
	// Not agents.error, because the two staleness signals answer different
	// questions. A stale SIDECAR means the binary executing inside the
	// container is older than this server — memory recall and egress policy are
	// degraded right now, and it is one agent's container misbehaving.
	// A stale IMAGE means the container is a faithful snapshot of an older
	// release: nothing is malfunctioning, the fleet is simply behind. Someone
	// who mutes agents.error to stop run-failure noise must not thereby lose
	// "every crew on this instance is three releases old", which is an
	// instance-health fact — exactly what system.health names, and its first
	// journal producer.
	journal.EntryImageStale:      notify.CategorySystemHealth,
	journal.EntrySystemMigration: notify.CategorySystemMigration,
	journal.EntryGuardrailInput:  notify.CategorySecurity,
	journal.EntryGuardrailOutput: notify.CategorySecurity,
}

// neverNotify names entry types that must NEVER produce a category, checked
// ahead of the map so a careless addition cannot re-enable them.
//
// The notification.* types are the delivery records this very bridge causes.
// Routing one back into the router would notify about having notified, and
// that notification would emit another delivery record — an unbounded loop
// that would hammer every configured channel.
var neverNotify = map[journal.EntryType]bool{
	journal.EntryNotificationDelivered: true,
	journal.EntryNotificationFailed:    true,
	journal.EntryNotificationDropped:   true,
}

// CategoryForJournalType resolves a journal entry type to its notification
// category, or "" when that type must not notify.
func CategoryForJournalType(t journal.EntryType) string {
	if neverNotify[t] {
		return ""
	}
	return journalCategories[t]
}

// CategoryForJournalEntry resolves an entry to its category, taking into
// account what the entry itself says about whether it is worth announcing.
//
// Today that is one claim: a completed run whose routine already told the
// whole workspace it finished. That message says what happened AND carries
// the result; the generic "Pipeline x completed" behind it is the same news
// said worse, and getting both for every run is how a channel earns a mute.
//
// The claim is made by the producer (the executor knows its own steps) rather
// than inferred here, and it applies only to the entry type it is about — a
// payload carrying self_announced on something else must not silence that.
func CategoryForJournalEntry(e journal.Entry) string {
	category := CategoryForJournalType(e.Type)
	if category == "" {
		return ""
	}
	if e.Type == journal.EntryPipelineRunCompleted {
		if claimed, _ := e.Payload["self_announced"].(bool); claimed {
			return ""
		}
	}
	return category
}

// severityPriority maps a journal severity onto the priority scale a
// channel's min_priority floor compares against. An observational event
// carries no priority of its own, and defaulting everything to "medium"
// would make the floor meaningless for exactly the events it exists to
// filter.
func severityPriority(s journal.Severity) string {
	switch s {
	case journal.SeverityError:
		return "high"
	case journal.SeverityWarn:
		return "medium"
	default: // info, notice, unset
		return "low"
	}
}

// ObserveJournal is the journal commit observer, registered via
// journal.Writer.AddCommitObserver at boot. It runs INLINE on the journal
// write path, so triage here is a single map lookup per entry; anything
// routable is handed to the existing fan-out, which already detaches onto its
// own goroutine.
//
// It must not block and must not retain the slice — the writer reuses the
// backing array once this returns, so every entry that survives triage is
// projected into a fresh value before it escapes.
func (r *Router) ObserveJournal(entries []journal.Entry) {
	if r == nil || r.db == nil {
		return
	}
	for i := range entries {
		category := CategoryForJournalEntry(entries[i])
		if category == "" {
			continue
		}
		if entries[i].WorkspaceID == "" {
			continue // nothing to scope an audience to
		}
		item := journalItem(entries[i], category)
		r.notifyItem(context.Background(), category, item)
	}
}

// journalItem projects a journal entry onto the inbox.Item shape the router
// already routes. Reusing that shape rather than introducing a parallel type
// keeps ONE routing path: preference matrix, allowlist, priority floor, rate
// gate and delivery log all keep working unchanged, and a future change to
// any of them cannot apply to one producer and silently miss the other.
func journalItem(e journal.Entry, category string) inbox.Item {
	title := strings.TrimSpace(e.Summary)
	if title == "" {
		title = string(e.Type)
	}
	// The entry's OWN facts first — duration, cost, which routine, which
	// finding. This used to be dropped: the payload was built from the
	// entry's identity alone, so a notification had nothing to say beyond
	// its summary and nothing for a template to say it with either.
	payload := make(map[string]any, len(e.Payload)+5)
	for k, v := range e.Payload {
		payload[k] = v
	}
	// Identity second, so it wins: these are what links resolve from, and an
	// entry whose payload happens to carry "run_id" must not be able to
	// repoint the notification's bookkeeping at something else.
	payload["journal_entry_id"] = e.ID
	payload["entry_type"] = string(e.Type)
	for k, v := range map[string]string{
		"crew_id":    e.CrewID,
		"agent_id":   e.AgentID,
		"mission_id": e.MissionID,
	} {
		if v != "" {
			payload[k] = v
		}
	}
	return inbox.Item{
		BodyMD:      journalBody(e.Payload),
		WorkspaceID: e.WorkspaceID,
		// Not a real inbox kind — nothing is written to inbox_items on this
		// path. It labels the delivery-log row's source, and the prefix keeps
		// the dedup key from ever colliding with a real inbox item that
		// happens to share an id.
		Kind:       "journal:" + string(e.Type),
		SourceID:   e.ID,
		Title:      title,
		SenderType: "system",
		SenderName: string(e.Type),
		Priority:   severityPriority(e.Severity),
		Payload:    payload,
	}
}

// journalBodyMaxFacts bounds how many facts a journal-sourced notification
// prints. A payload is producer-authored and can be large — a lookout entry
// carries every finding — while a notification is a chat message someone
// glances at.
const journalBodyMaxFacts = 12

// journalBody renders an entry's payload as the body of its notification.
//
// Journal-sourced messages had no body at all: they arrived as a bare summary
// line, which told a reader that something happened and nothing about what.
// The facts to fix that were already on the entry.
//
// Keys ending in _id are omitted. They are how links and machines find things,
// and a chat message that spends a line on "crew_id: crew_8f2a" has traded
// something a person can act on for something they cannot. The suffix is the
// convention every producer in this codebase already follows, so the rule does
// not need a list that lags them.
//
// This is deliberately a plain fact list rather than per-entry-type prose.
// Wording belongs to the message-template layer; this exists so that until it
// lands, a notification says something.
func journalBody(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		if strings.HasSuffix(k, "_id") {
			continue
		}
		if s, ok := payload[k].(string); ok && strings.TrimSpace(s) == "" {
			continue // an empty value is not a fact
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys) // stable output; a map's order would change per send
	if len(keys) > journalBodyMaxFacts {
		keys = keys[:journalBodyMaxFacts]
	}
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s: %v", k, payload[k])
	}
	return b.String()
}

// emitDeliveryJournal records an outbound delivery attempt on the journal so
// it lands on the Activity timeline with its own icon — "this left the
// instance and went to Slack" is a side effect an operator should see next to
// the event that caused it, not one buried in an admin-only deliveries table.
//
// Best-effort: a journal write must never fail a delivery that already
// happened.
func (r *Router) emitDeliveryJournal(ctx context.Context, entryType journal.EntryType, sev journal.Severity, ch notify.Channel, category, title, detail string) {
	if r.journal == nil {
		return
	}
	target := string(ch.Type)
	if ch.Provider != "" {
		target = ch.Provider
	}
	// The title reaching here is the producer's, unredacted — the delivery
	// path scrubs a COPY of the envelope, not this. A journal entry is a
	// second egress: the Activity timeline renders it, exports and backups
	// carry it. See notify.ScrubText.
	title = notify.ScrubText(title)
	detail = notify.ScrubText(detail)

	payload := map[string]any{
		"channel_id":   ch.ID,
		"channel_type": string(ch.Type),
		"category":     category,
		"title":        title,
		"target":       target,
	}
	if detail != "" {
		payload["detail"] = detail
	}

	var summary string
	switch entryType {
	case journal.EntryNotificationDelivered:
		summary = fmt.Sprintf("Sent %s notification to %s", category, target)
	case journal.EntryNotificationFailed:
		summary = fmt.Sprintf("Failed to send %s notification to %s: %s", category, target, detail)
	default:
		summary = fmt.Sprintf("Dropped %s notification for %s (%s)", category, target, detail)
	}

	if _, err := r.journal.Emit(ctx, journal.Entry{
		WorkspaceID: ch.WorkspaceID,
		Type:        entryType,
		Severity:    sev,
		ActorType:   journal.ActorSystem,
		ActorID:     "notify",
		Summary:     summary,
		Payload:     payload,
	}); err != nil {
		r.logger.Warn("notifyroute: emit delivery journal entry", "error", err, "channel_id", ch.ID)
	}
}

// journalEmitter is the narrow slice of journal.Emitter the router uses.
// Kept as an interface so notifyroute doesn't depend on the concrete writer
// and tests can assert on what was emitted.
type journalEmitter interface {
	Emit(ctx context.Context, e journal.Entry) (string, error)
}

// SetJournal wires the emitter used for delivery records. Optional: a nil
// emitter means deliveries simply don't appear on the Activity timeline,
// which is how the existing test rigs run.
func (r *Router) SetJournal(j journalEmitter) { r.journal = j }
