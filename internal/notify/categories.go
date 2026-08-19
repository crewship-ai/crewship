package notify

// Category is one of the user-facing notification categories — the
// vocabulary shared by the preference matrix (user_notification_prefs.category),
// the admin per-channel allowlist (notification_channels.categories_json), and
// the notification_deliveries log.
//
// # Why this list is shaped the way it is
//
// The #1412 MVP shipped 9 categories, but only 5 of them had a producer:
// categoryByKind (below) is the ONLY code anywhere that mints a category, and
// it maps inbox kinds. `runs.completed`, `security`, `budget` and `system` had
// no inbox kind and no other source, so they rendered as switchable rows in the
// matrix that could never deliver anything — 44% of the grid was decorative.
// Issues had no coverage at all.
//
// The fix is not more inbox kinds. The inbox is a HUMAN-ATTENTION QUEUE: every
// row is a card someone is meant to read and often act on. That is right for
// approvals and escalations and wrong for "budget crossed 80%" or "an issue
// moved to In Review" — nobody wants forty inbox cards a day, but they may well
// want those in Slack. So there are two producers feeding one vocabulary:
//
//	inbox kind    → category   (actionable; categoryByKind)
//	journal type  → category   (observational; internal/notifyroute's bridge)
//
// Everything downstream — prefs, allowlist, priority floor, rate gate, delivery
// log — keys off the category and does not care which producer minted it.
//
// Approvals and escalations always deliver immediately and bypass the
// anti-storm rate gate (see BypassesRateGate): a blocking HITL item must never
// be silently dropped as "too many notifications". The rest are digest-eligible
// in the sense that a v2 digest window COULD batch them; MVP only ever writes
// state='immediate' or 'off'.
const (
	// Routines — a scheduled or triggered routine run.
	CategoryRoutinesCompleted = "routines.completed"
	CategoryRoutinesFailed    = "routines.failed"
	CategoryRoutinesSkipped   = "routines.skipped"
	// CategoryRoutinesMissed covers both "the schedule dropped backlog
	// occurrences" (inbox schedule_missed) and "the circuit breaker
	// auto-disabled this schedule" (inbox schedule_circuit_breaker_tripped).
	// Both answer the same user question: why did my routine stop running?
	CategoryRoutinesMissed = "routines.missed"

	// Issues — the mission/issue tracker. `issues.state` carries blocked/
	// unblocked transitions too; a separate `issues.blocked` row would split
	// one user intent ("tell me when this moves") across two toggles.
	CategoryIssuesCreated  = "issues.created"
	CategoryIssuesState    = "issues.state"
	CategoryIssuesAssigned = "issues.assigned"
	CategoryIssuesComment  = "issues.comment"

	// Agents — what an agent needs from a human, or what went wrong with one.
	CategoryAgentsApproval   = "agents.approval"
	CategoryAgentsEscalation = "agents.escalation"
	CategoryAgentsError      = "agents.error"
	CategoryAgentsBudget     = "agents.budget"

	// System + security — instance-level events.
	CategorySystemHealth    = "system.health"
	CategorySystemMigration = "system.migration"
	CategorySecurity        = "security"

	// CategoryPagesStale fires when a Page's panel data ages past its SLA
	// (docs/prd/pages.md §10b.6). It notifies the page owner only — default
	// on for the owner, off for everyone else; `on_failure` → issue remains
	// the escalation path for anything that needs work rather than
	// awareness.
	CategoryPagesStale = "pages.stale"

	// Everything else.
	CategoryChatReplies = "chat.replies"
	CategoryMemory      = "memory"

	// CategoryMuteAll is the per-channel "mute everything" sentinel cell in
	// the preference matrix — not a real notification category, never
	// appears on a delivery or a channel's admin allowlist.
	CategoryMuteAll = "*"

	// CategoryAgentsMessage labels a message an AGENT sent of its own accord
	// through the notify_send tool.
	//
	// Deliberately NOT in AllCategories, so it never appears as a row in the
	// preference matrix. Everything else in this file describes an EVENT a
	// user chooses to subscribe to; an agent send is not an event, it is a
	// direct message to a channel a human explicitly paired that agent with.
	// The pairing IS the authorization (see agent_pairing.go), so routing it
	// through the matrix as well would mean someone's mute could silently
	// swallow a message an admin had already approved — confusing from both
	// ends, and it would make the pairing look like it had not worked.
	//
	// It exists as a constant so the delivery log and the Activity entry can
	// say where a message came from.
	CategoryAgentsMessage = "agents.message"
)

// CategoryGroup labels the UI grouping a category belongs to. The matrix is
// large enough that flat rendering is unusable; the settings grid and
// `notify prefs` both collapse by group.
type CategoryGroup struct {
	Key        string   // stable id, safe for CSS/test selectors
	Label      string   // human-readable heading
	Categories []string // ordered members
}

// CategoryGroups is the ordered grouping the UI renders. Every real category
// appears in exactly one group — TestCategoryGroupsCoverAllCategories pins
// that, so a category added without a home fails CI rather than silently
// vanishing from the settings page.
var CategoryGroups = []CategoryGroup{
	{Key: "routines", Label: "Routines", Categories: []string{
		CategoryRoutinesCompleted, CategoryRoutinesFailed,
		CategoryRoutinesSkipped, CategoryRoutinesMissed,
	}},
	{Key: "issues", Label: "Issues", Categories: []string{
		CategoryIssuesCreated, CategoryIssuesState,
		CategoryIssuesAssigned, CategoryIssuesComment,
	}},
	{Key: "agents", Label: "Agents", Categories: []string{
		CategoryAgentsApproval, CategoryAgentsEscalation,
		CategoryAgentsError, CategoryAgentsBudget,
	}},
	{Key: "system", Label: "System", Categories: []string{
		CategorySystemHealth, CategorySystemMigration, CategorySecurity,
		CategoryPagesStale,
	}},
	{Key: "other", Label: "Chat & memory", Categories: []string{
		CategoryChatReplies, CategoryMemory,
	}},
}

// AllCategories is the fixed, ordered vocabulary of real (non-sentinel)
// categories — the row set the settings matrix and `notify prefs` render.
// Derived from CategoryGroups so the two can never disagree.
var AllCategories = func() []string {
	var out []string
	for _, g := range CategoryGroups {
		out = append(out, g.Categories...)
	}
	return out
}()

// LegacyCategories maps each pre-taxonomy-v2 (#1412) category name to its
// replacement(s). Used by migration v169 to rewrite user_notification_prefs
// and notification_channels.categories_json in place, and by the deliveries
// reader to display historical rows that still carry the old vocabulary.
//
// A user who opted a cell in must stay opted in, and one who muted must stay
// muted — so this is a rewrite, never a drop. `system` fans out to BOTH new
// system categories: it had no producer, so a pref row for it expresses the
// intent "tell me about system things", and collapsing that onto one of the
// two would silently discard half of what was asked for.
var LegacyCategories = map[string][]string{
	"approvals":      {CategoryAgentsApproval},
	"escalations":    {CategoryAgentsEscalation},
	"runs.failed":    {CategoryRoutinesFailed},
	"runs.completed": {CategoryRoutinesCompleted},
	"budget":         {CategoryAgentsBudget},
	"system":         {CategorySystemHealth, CategorySystemMigration},
	// Unchanged by the rename, listed so the map is a complete statement of
	// the old vocabulary rather than a partial diff a reader has to reconcile.
	"chat.replies": {CategoryChatReplies},
	"security":     {CategorySecurity},
	"memory":       {CategoryMemory},
}

// immediateOnlyCategories are the categories that bypass the anti-storm rate
// gate (they still respect the user's off/immediate preference and the admin
// allowlist — only the TOKEN BUCKET is skipped): a blocking HITL item must
// never be silently dropped as "too many notifications".
var immediateOnlyCategories = map[string]bool{
	CategoryAgentsApproval:   true,
	CategoryAgentsEscalation: true,
}

// BypassesRateGate reports whether category is exempt from the per
// (recipient, channel, category) token bucket.
func BypassesRateGate(category string) bool {
	return immediateOnlyCategories[category]
}

// validCategories indexes AllCategories for O(1) lookup.
var validCategories = func() map[string]bool {
	m := make(map[string]bool, len(AllCategories))
	for _, c := range AllCategories {
		m[c] = true
	}
	return m
}()

// ValidCategory reports whether c is one of the real categories (the mute-all
// sentinel is deliberately excluded — it is a cell state, not a selectable
// category in the allowlist sense, though it IS legal on a
// user_notification_prefs row; see ValidPrefCategory).
func ValidCategory(c string) bool { return validCategories[c] }

// ValidPrefCategory reports whether c is legal on a user_notification_prefs
// row: any real category, or the mute-all sentinel.
func ValidPrefCategory(c string) bool {
	return c == CategoryMuteAll || ValidCategory(c)
}

// GroupForCategory returns the group key a category belongs to, or "" for an
// unknown category.
func GroupForCategory(c string) string {
	for _, g := range CategoryGroups {
		for _, m := range g.Categories {
			if m == c {
				return g.Key
			}
		}
	}
	return ""
}

// categoryByKind maps an internal/inbox Kind constant to the notification
// category it fans out to. Kept here (rather than in internal/inbox, a leaf
// package with no notify-vocabulary dependency) so inbox stays decoupled from
// the category model — see internal/notifyroute's router, the only caller.
//
// This covers the ACTIONABLE half of the vocabulary. Observational categories
// (issues.*, agents.error, agents.budget, system.*, security,
// routines.completed) are fed from the journal instead — see
// notifyroute.CategoryForJournalType.
var categoryByKind = map[string]string{
	"waitpoint":                        CategoryAgentsApproval,
	"escalation":                       CategoryAgentsEscalation,
	"failed_run":                       CategoryRoutinesFailed,
	"message":                          CategoryChatReplies,
	"memory_consolidation":             CategoryMemory,
	"schedule_missed":                  CategoryRoutinesMissed,
	"schedule_circuit_breaker_tripped": CategoryRoutinesMissed,
}

// SubkindRoutineUpdate is the payload discriminator a notify step writes so its
// progress notices stay out of the approval lane. It is duplicated as a literal
// in internal/pipeline (the producer) because inbox is a leaf package neither
// side may import from the other; TestRoutineUpdateSubkindMatchesProducer keeps
// the two spellings honest.
const SubkindRoutineUpdate = "routine_update"

// CategoryForItem resolves an inbox row to its notification category.
//
// Kind alone is not always enough. A chat reply and a routine's progress notice
// are both kind=message, so mapping by kind put every "step build finished"
// into chat.replies — the category a person tunes for "an agent answered me
// while I was away". Muting one muted the other, and the routines.* rows they
// thought they had subscribed to never arrived.
//
// The discriminator already exists: runner_notify writes subkind=routine_update
// for exactly these. Reading it here keeps the vocabulary in the one file that
// owns it rather than pushing a default back into the producer.
func CategoryForItem(kind string, payload map[string]interface{}) string {
	if kind == "message" {
		if sub, _ := payload["subkind"].(string); sub == SubkindRoutineUpdate {
			return CategoryRoutinesCompleted
		}
	}
	return CategoryForKind(kind)
}

// CategoryForKind resolves an inbox kind to its notification category.
// Returns "" for a kind with no mapping (nothing to route externally — still
// lands in the in-product inbox as before).
func CategoryForKind(kind string) string {
	return categoryByKind[kind]
}

// PriorityRank orders inbox/channel priority levels low→urgent so the router
// can compare an item's priority against a channel's min_priority floor.
// Unknown values rank as "low" (never silently over-deliver past an admin's
// floor because of a typo'd priority).
func PriorityRank(p string) int {
	switch p {
	case "urgent":
		return 3
	case "high":
		return 2
	case "medium":
		return 1
	default: // "low", "", unknown
		return 0
	}
}
