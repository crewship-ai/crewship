package notifyroute

import (
	"context"
	"fmt"
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

	// Agents. keeper.request is absent — it already becomes an inbox
	// escalation, so mapping it here would double-deliver.
	journal.EntryAgentError:              notify.CategoryAgentsError,
	journal.EntryProvisioningFailed:      notify.CategoryAgentsError,
	journal.EntryProvisioningBuildFailed: notify.CategoryAgentsError,
	journal.EntrySidecarStale:            notify.CategoryAgentsError,
	journal.EntryBudgetExceed:            notify.CategoryAgentsBudget,
	journal.EntryBudgetWarning:           notify.CategoryAgentsBudget,

	// System + security.
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
		category := CategoryForJournalType(entries[i].Type)
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
	payload := map[string]any{
		"journal_entry_id": e.ID,
		"entry_type":       string(e.Type),
	}
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
