package automation

import (
	"context"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The second thing an automation may do: open an issue.
//
// Until now a matched rule could only park a deferred routine run, and
// types.go says why the column allowed more than that: "The column exists so
// 'issue' / 'notify' can land without a migration". This is 'issue' landing.
//
// WHY IT IS HERE AND NOT A SECOND EVENTING PATH.
// Pages' wake gates (docs/prd/pages.md §5) need journal event → predicate →
// debounced, rate-limited, coalesced action, which is precisely what this
// package already does and what a second observer on the journal write path
// would have had to reimplement — badly, because the hard parts of it are the
// coalescing and the burst brake rather than the matching. A wake gate
// therefore compiles to an ordinary `automations` row whose action opens an
// issue on the crew the page author named.
//
// WHAT AN ISSUE ACTION STILL MAY NOT DO.
// The same thing every automation may not do: execute anything. It creates a
// piece of work in a place a human and a crew's lead agent both look, and the
// existing issue lifecycle decides whether anybody starts it. Nothing here
// runs an agent, and nothing here holds a veto.
//
// THE LOOP QUESTION.
// A routine action is priced against pipeline.GuardChainDepth because it
// creates a run, and a run can cause the entry that fires the next hop. An
// issue creates no run, so there is no chain to price and no depth to inherit;
// what bounds it instead is (a) max_per_hour, the same burst brake every rule
// carries, and (b) the OPENER, which is expected to refuse to open a second
// issue while the first one for the same subject is still open. Pages does
// exactly that with its page_panel_alerts row. An opener that skips it will
// produce one issue per debounce window for as long as the condition holds,
// which is loud rather than unbounded — but it is the opener's job, and this
// package cannot do it because only the opener knows what "the same subject"
// means.

// ActionKindIssue names the action that opens an issue. The value matches the
// vocabulary types.go reserved for it, so a row written by this release reads
// the same way to the release that anticipated it.
const ActionKindIssue = "issue"

// IssueAction is the decoded action_config_json for ActionKindIssue.
//
// Title and Body may reference the triggering entry with the same
// {{ event.* }} namespace routine inputs use (see EventContext). There is
// deliberately no assignee, no priority ladder and no label list here: an
// automation names WHO owns the work (a crew) and WHAT it is, and everything
// else about an issue is the issue tracker's own vocabulary, which the opener
// applies with its own defaults rather than this rule carrying a second copy
// of them.
type IssueAction struct {
	// CrewSlug is the crew whose board gets the issue. A crew and never a
	// user or a single agent: the crew is the durable subject, and a rule
	// pointed at somebody who leaves is a rule that silently stops working.
	CrewSlug string `json:"crew_slug"`
	// Title is the issue's one-line summary.
	Title string `json:"title"`
	// Body is the markdown description. Optional, and in practice never
	// omitted — an issue that says only "panel x is critical" makes whoever
	// opens it go and find out where x is.
	Body string `json:"body,omitempty"`
	// DedupeKey identifies the SUBJECT of the issue for an opener that
	// refuses duplicates. Opaque to this package.
	DedupeKey string `json:"dedupe_key,omitempty"`
	// Context is opener-specific detail carried verbatim. Pages puts the
	// page, panel and gate in here so the opener can find the panel without
	// parsing the title.
	Context map[string]string `json:"context,omitempty"`
}

// IssueIntent is one coalesced decision to open an issue, handed to the
// opener by Flush — off the journal write path, like every other I/O this
// package performs.
type IssueIntent struct {
	WorkspaceID    string
	AutomationID   string
	AutomationName string
	// EventType is the journal type that fired the rule, so an opener can say
	// in the issue why it exists.
	EventType string
	CrewSlug  string
	Title     string
	Body      string
	DedupeKey string
	Context   map[string]string
	// Coalesced counts the matched entries that folded into this one issue,
	// answering "why one issue for 200 events" from the issue itself.
	Coalesced int
}

// IssueOpener is the sink for ActionKindIssue. Implemented in internal/api,
// which is where issue creation lives; declared as an interface here for the
// same reason Enqueuer is — so a test can count exactly how many issues one
// burst produced, which is the property the coalescing exists to hold.
//
// An opener that declines because an issue is already open for the same
// subject reports (false, nil): not an error, and not a silent success either,
// so the registry can log the difference.
type IssueOpener interface {
	OpenIssue(ctx context.Context, in IssueIntent) (opened bool, err error)
}

// SetIssueOpener installs the sink for issue-kind rules. Without one such a
// rule matches, coalesces and then reports — loudly — that it had nowhere to
// go, rather than dropping the match in silence.
func (r *Registry) SetIssueOpener(o IssueOpener) { r.issues = o }

// renderIssueIntent builds the intent for one matched entry, substituting
// {{ event.* }} in the title and body with pipeline.Render — the same renderer
// routine steps and automation inputs use, for the reason RenderInputs states:
// a second templating language would drift from the first.
func renderIssueIntent(rule Resolved, e journal.Entry) *IssueIntent {
	cfg := rule.Action.Issue
	if cfg == nil {
		// Unreachable through Validate, and a rule that reached here without
		// its config must not take the flusher down with it.
		cfg = &IssueAction{}
	}
	rctx := pipeline.RenderContext{Event: EventContext(e)}
	return &IssueIntent{
		WorkspaceID:    rule.WorkspaceID,
		AutomationID:   rule.ID,
		AutomationName: rule.Name,
		EventType:      rule.EventType,
		CrewSlug:       cfg.CrewSlug,
		Title:          pipeline.Render(cfg.Title, rctx),
		Body:           pipeline.Render(cfg.Body, rctx),
		DedupeKey:      cfg.DedupeKey,
		Context:        cfg.Context,
	}
}

// openIssue hands one coalesced intent to the opener. Returns whether an issue
// was actually created, which is what Flush counts.
//
// Every outcome is reported. A rule that matched and produced nothing is the
// failure this package is most afraid of, so "there is no opener wired" and
// "the opener declined" say so in the log rather than looking like success.
func (r *Registry) openIssue(ctx context.Context, it *intent) bool {
	if it.issue == nil {
		return false
	}
	if r.issues == nil {
		r.logger.Error("automation: issue rule matched but no issue opener is wired",
			"automation_id", it.automationID, "automation_name", it.automationName,
			"crew", it.issue.CrewSlug)
		return false
	}
	opened, err := r.issues.OpenIssue(ctx, *it.issue)
	if err != nil {
		r.logger.Error("automation: opening the issue failed",
			"err", err, "automation_id", it.automationID, "crew", it.issue.CrewSlug)
		return false
	}
	if !opened {
		r.logger.Info("automation: issue already open for this subject, not opening a second",
			"automation_id", it.automationID, "dedupe_key", it.issue.DedupeKey)
	}
	return opened
}

// validateIssueAction checks the half of an automation only this kind uses.
func validateIssueAction(a *IssueAction) error {
	if a == nil {
		return errors.New("automation: action.issue required for action_kind 'issue'")
	}
	if strings.TrimSpace(a.CrewSlug) == "" {
		return errors.New("automation: action.issue.crew_slug required")
	}
	if strings.TrimSpace(a.Title) == "" {
		return errors.New("automation: action.issue.title required")
	}
	return nil
}
