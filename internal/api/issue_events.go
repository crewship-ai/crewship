package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ── Issue activity: one vocabulary, one emitter ─────────────────────────────
//
// Everything that happens to an issue has to land in three places, and before
// #1768 F1 each writer picked its own subset:
//
//	mission_activity  the row the issue's Activity timeline reads
//	journal           the entry the NOTIFICATION ROUTER reads
//	ws.Hub            the live "this board changed" nudge
//
// There were three logActivity implementations — IssueHandler's (all three),
// InternalIssueHandler's (no journal), CodeLinkHandler's (no journal, and a
// different signature that derived actor type itself). Because
// internal/notifyroute routes per JOURNAL ENTRY TYPE, the missing journal
// emit was not a cosmetic gap: no action an agent took on the issue board
// could ever produce a notification. This file is the single emitter all
// three now go through.

// issueAction is the closed vocabulary of mission_activity.action values.
//
// The column is TEXT NOT NULL with no CHECK constraint
// (internal/database/migrate_consts_v33_v41.go), so nothing in the schema
// keeps writers honest — a typo'd action is a silently unroutable, unlabelled
// timeline row. Naming the set in one place is the substitute for the
// constraint the table does not have: journalTypeForIssueAction switches on
// these, the frontend's actionLabel switches on the same strings, and
// knownIssueActions lets a test assert the two stay in step.
type issueAction string

const (
	// ── in use today ────────────────────────────────────────────────────
	// Written by issue_handler_create.go.
	actionCreated issueAction = "created"
	// Written by the human PATCH (issue_handler_update.go), the agent PATCH
	// (issues_internal.go) and bulk edit (issue_handler_bulk.go).
	actionStatusChanged   issueAction = "status_changed"
	actionAssigneeChanged issueAction = "assignee_changed"
	actionPriorityChanged issueAction = "priority_changed"
	// Written by the agent-facing relations endpoint
	// (issues_internal_relations.go).
	actionParentChanged issueAction = "parent_changed"
	actionRelationAdded issueAction = "relation_added"
	// Written by the review workflow (issue_handler_workflow.go).
	actionReviewApproved         issueAction = "review_approved"
	actionReviewChangesRequested issueAction = "review_changes_requested"
	// Written by the run/orchestration paths (assignments_run.go,
	// orchestrator/mission_tasks_completion.go). Those two still INSERT into
	// mission_activity directly rather than coming through this emitter —
	// they are named here because they are part of the vocabulary the UI and
	// the journal mapping have to cover, not because this file writes them.
	actionTaskCompleted issueAction = "task_completed"
	actionTaskFailed    issueAction = "task_failed"
	// Reserved by journalTypeForIssueAction since #1519 but never written by
	// anything: comments go through mission_comments, not mission_activity.
	actionCommented issueAction = "commented"

	// ── defined now, called later ───────────────────────────────────────
	// F1 defines the plumbing so the PRs that follow only add call sites.
	//
	// descriptionChanged is wired in this PR (issue_handler_update.go); the
	// other three are not, and are here so their journal mapping, their UI
	// label and their notification routing are decided once rather than
	// re-litigated per PR.
	actionDescriptionChanged issueAction = "description_changed"
	// mentioned: an @-mention of an agent or user in an issue body or
	// comment. lib/mentions.ts already renders this exact string
	// (MENTION_ACTIONS) — the frontend was written ahead of the producer.
	actionMentioned issueAction = "mentioned"
	// attachmentAdded / attachmentRemoved: a file attached to, or detached
	// from, the issue. Written by issue_attachments.go (the human/CLI door) and
	// issue_attachments_internal.go (the agent's). The pair exists for the same
	// reason codeLinkAdded/codeLinkRemoved does: an attachment that is gone from
	// the issue but was never recorded as removed reads, on the timeline, as an
	// attachment that was never there.
	actionAttachmentAdded   issueAction = "attachment_added"
	actionAttachmentRemoved issueAction = "attachment_removed"
	// codeLinkAdded / codeLinkRemoved: a PR/MR linked to the issue. These
	// two are NOT new — issue_code_links.go has written them since #1758;
	// they are listed under "called later" only in the sense that the
	// enumeration is new, the strings are not.
	actionCodeLinkAdded   issueAction = "code_link_added"
	actionCodeLinkRemoved issueAction = "code_link_removed"
)

// knownIssueActions is the enumeration as data, so a test can assert that
// every constant above has a deliberate journal mapping instead of falling
// into the catch-all by accident.
var knownIssueActions = []issueAction{
	actionCreated,
	actionStatusChanged,
	actionAssigneeChanged,
	actionPriorityChanged,
	actionParentChanged,
	actionRelationAdded,
	actionReviewApproved,
	actionReviewChangesRequested,
	actionTaskCompleted,
	actionTaskFailed,
	actionCommented,
	actionDescriptionChanged,
	actionMentioned,
	actionAttachmentAdded,
	actionAttachmentRemoved,
	actionCodeLinkAdded,
	actionCodeLinkRemoved,
}

// issueEvent is one auditable thing that happened to one issue.
//
// ActorType is the mission_activity vocabulary ("user" / "agent" /
// "system"), not the journal's — issueEvents.log maps it, defaulting
// anything unrecognised to system rather than writing a journal entry that
// claims an actor kind the journal does not define.
type issueEvent struct {
	MissionID string
	ActorType string
	ActorID   string
	Action    issueAction
	Details   string
	// From/To carry a status transition as data. Details keeps the prose
	// ("BACKLOG → TODO") because that is what a human reads in the timeline;
	// these are what an automation matcher can predicate on. Both emit sites
	// already held them and joined them into the sentence, so the structure
	// was being thrown away — which is why "fire when an issue moves to DONE"
	// was not expressible at all.
	From string
	To   string
}

// issueEventPayload is the journal payload for one issue event.
//
// Extracted so the shape has a name and a test: the automation matcher
// predicates on these keys, and until they were pinned the docs described a
// key (`to`) the emitter never produced.
func issueEventPayload(ev issueEvent) map[string]any {
	p := map[string]any{"action": string(ev.Action), "details": ev.Details}
	// Only on a transition. A key that is always present and always empty is
	// a predicate that always fails, which is the failure mode this whole
	// change exists to remove.
	if ev.From != "" {
		p["from"] = ev.From
	}
	if ev.To != "" {
		p["to"] = ev.To
	}
	return p
}

// issueEvents fans an issue event out to the audit row, the journal and the
// hub. It is a value, not a long-lived dependency: handlers build one per
// call from the fields they already hold (see IssueHandler.events), so
// wiring a journal after construction via SetJournal keeps working.
//
// Every write here is best-effort. The mutation being audited has already
// been committed by the time the caller gets here, and a failed bookkeeping
// row must never be allowed to look like a failed mutation.
type issueEvents struct {
	db      *sql.DB
	hub     *ws.Hub
	logger  *slog.Logger
	journal journal.Emitter
}

// log records a single event in mission_activity and the journal. It does
// NOT broadcast — call sites that batch several field changes into one
// mutation want one "the issue changed" nudge, not one per field; see
// record.
func (e issueEvents) log(ctx context.Context, ev issueEvent) {
	actID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actID, ev.MissionID, ev.ActorType, ev.ActorID, string(ev.Action), ev.Details, now); err != nil {
		e.logf("insert mission activity", ev, err)
	}

	if e.journal == nil {
		return
	}

	// The mission row carries workspace_id and crew_id; we grab them here
	// rather than threading them through every call site. One extra light
	// query per event is cheap next to a single-shape signature everywhere.
	var workspaceID, crewID string
	_ = e.db.QueryRowContext(ctx,
		`SELECT workspace_id, crew_id FROM missions WHERE id = ?`, ev.MissionID).
		Scan(&workspaceID, &crewID)
	if workspaceID == "" {
		// Some legacy callers pass a chat id as the mission id, and the
		// journal write needs a workspace to be tenant-scoped at all. Skip
		// silently: the activity row above already landed.
		return
	}

	actor := journal.ActorType(ev.ActorType)
	if actor != journal.ActorAgent && actor != journal.ActorUser && actor != journal.ActorSystem {
		actor = journal.ActorSystem
	}
	_, _ = e.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		MissionID:   ev.MissionID,
		Type:        journalTypeForIssueAction(ev.Action),
		Severity:    journal.SeverityInfo,
		ActorType:   actor,
		ActorID:     ev.ActorID,
		Summary:     string(ev.Action) + ": " + truncate(ev.Details, 120),
		Payload:     issueEventPayload(ev),
		Refs:        map[string]any{"mission_id": ev.MissionID, "activity_id": actID},
	})
}

// record logs every event of one mutation and then announces the issue once.
//
// Zero events is legal and meaningful: a label-only or comment-only PATCH
// changes nothing auditable at the field level but every open board still
// has to redraw. Keeping that case in the same call is what stops the
// broadcast from drifting away from the audit trail again.
func (e issueEvents) record(ctx context.Context, wsID string, payload map[string]string, evs ...issueEvent) {
	for _, ev := range evs {
		e.log(ctx, ev)
	}
	broadcastWorkspaceEvent(e.hub, wsID, "issue.updated", payload)
}

// logf reports a failed audit write without assuming a logger was wired —
// CodeLinkHandler and the issue handlers all carry one, but the emitter is
// constructed from struct fields and a nil here would turn a best-effort
// bookkeeping miss into a panic on the response path.
func (e issueEvents) logf(msg string, ev issueEvent, err error) {
	if e.logger == nil {
		return
	}
	e.logger.Error(msg, "action", string(ev.Action), "mission_id", ev.MissionID, "error", err)
}

// describeDescriptionChange builds the `details` payload for a
// description_changed event.
//
// It deliberately carries NO description text, only rune counts. The
// alternatives were considered and rejected:
//
//   - the full before/after is unbounded user (or model) input landing in an
//     audit row that is read back into the timeline, exported by backup, and
//     truncated into a journal Summary and a notification body. An issue
//     description is routinely kilobytes of markdown; two of them per edit
//     turns mission_activity into a revision store it was never shaped to be,
//     with no retention story;
//   - a truncated prefix of the new text is bounded but arbitrary — it shows
//     the first 120 characters, which is exactly the part an edit usually
//     does NOT touch, so it reads as "changed" next to text that looks
//     identical to the previous row. Worse, it is a partial copy of content
//     that the untrusted fence and the scrubber both have opinions about, now
//     duplicated somewhere neither of them runs.
//
// Rune counts answer what the audit row is actually for — who changed it,
// when, and roughly how much — and are the same length no matter what was
// typed. The description itself is already versionless on `missions`; if we
// ever want a real before/after it belongs in a revision table with its own
// retention, not smuggled into a TEXT column.
func describeDescriptionChange(oldText, newText string) string {
	oldN, newN := len([]rune(oldText)), len([]rune(newText))
	switch {
	case oldN == 0:
		return fmt.Sprintf("description set (%d chars)", newN)
	case newN == 0:
		return fmt.Sprintf("description cleared (was %d chars)", oldN)
	default:
		return fmt.Sprintf("description updated (%d → %d chars)", oldN, newN)
	}
}

// journalTypeForIssueAction picks the journal entry type for an issue
// activity action. Everything used to land as mission.status_change, which
// made "was assigned" and "was created" indistinguishable from "moved to In
// Review" — both on the Activity timeline (one icon for all three) and to the
// notification router, which routes per entry type. Splitting them is what
// lets a user subscribe to assignments without also getting every status
// change.
//
// Unrecognised actions keep the historical type: it stays the honest
// catch-all for "something about this issue changed", and — because
// notifyroute maps mission.status_change to issues.state — it is also the
// only default that still NOTIFIES. A new action that fell through to a type
// with no category would be audited and silently un-notifiable, which is the
// exact bug F1 exists to close.
func journalTypeForIssueAction(action issueAction) journal.EntryType {
	switch action {
	case actionCreated:
		return journal.EntryMissionCreated
	case actionAssigneeChanged:
		return journal.EntryMissionAssigned
	case actionCommented:
		return journal.EntryMissionComment
	case actionMentioned:
		// agent.mentioned is the semantically right type and has had no
		// producer until now. It is NOT in notifyroute's journalCategories
		// map, so a mention is journalled but not yet notified — adding the
		// category belongs with the PR that adds the mention call sites
		// (internal/mentions), not here.
		return journal.EntryAgentMentioned
	default:
		return journal.EntryMissionStatus
	}
}
