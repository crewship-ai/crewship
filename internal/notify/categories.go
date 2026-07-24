package notify

// Category is one of the ~9 user-facing notification categories (#1412),
// mapped from journal types / inbox kinds. This is the vocabulary the
// preference matrix (user_notification_prefs.category), the admin
// per-channel allowlist (notification_channels.categories_json), and the
// notification_deliveries log all share.
//
// Approvals and Escalations always deliver immediately (no v2 digest
// window applies to them) and bypass the anti-storm rate gate — see
// internal/notifyroute's router and ratelimit doc comments. The rest are
// "digest-eligible" in the sense that a v2 digest window COULD batch them;
// MVP only ever writes state='immediate' or 'off' for every category, the
// same as approvals/escalations — digest batching itself is v2 scope.
const (
	CategoryApprovals    = "approvals"
	CategoryEscalations  = "escalations"
	CategoryRunsFailed   = "runs.failed"
	CategoryRunsComplete = "runs.completed"
	CategoryChatReplies  = "chat.replies"
	CategorySecurity     = "security"
	CategoryBudget       = "budget"
	CategorySystem       = "system"
	CategoryMemory       = "memory"

	// CategoryMuteAll is the per-channel "mute everything" sentinel cell in
	// the preference matrix — not a real notification category, never
	// appears on a delivery or a channel's admin allowlist.
	CategoryMuteAll = "*"
)

// AllCategories is the fixed, ordered vocabulary of real (non-sentinel)
// categories — the row set the settings-page matrix and `notify prefs`
// render.
var AllCategories = []string{
	CategoryApprovals,
	CategoryEscalations,
	CategoryRunsFailed,
	CategoryRunsComplete,
	CategoryChatReplies,
	CategorySecurity,
	CategoryBudget,
	CategorySystem,
	CategoryMemory,
}

// ImmediateOnlyCategories are the categories that bypass the anti-storm
// rate gate (they still respect the user's off/immediate preference and
// the admin allowlist — only the TOKEN BUCKET is skipped): a blocking
// HITL item must never be silently dropped as "too many notifications."
var immediateOnlyCategories = map[string]bool{
	CategoryApprovals:   true,
	CategoryEscalations: true,
}

// BypassesRateGate reports whether category is exempt from the per
// (recipient, channel, category) token bucket.
func BypassesRateGate(category string) bool {
	return immediateOnlyCategories[category]
}

// ValidCategory reports whether c is one of the 9 real categories (the
// mute-all sentinel is deliberately excluded — it is a cell state, not a
// selectable category in the matrix's category-allowlist sense, though it
// IS a legal category value for user_notification_prefs rows; see
// ValidPrefCategory for that broader check).
func ValidCategory(c string) bool {
	for _, want := range AllCategories {
		if c == want {
			return true
		}
	}
	return false
}

// ValidPrefCategory reports whether c is legal on a user_notification_prefs
// row: any real category, or the mute-all sentinel.
func ValidPrefCategory(c string) bool {
	return c == CategoryMuteAll || ValidCategory(c)
}

// categoryByKind maps an internal/inbox Kind constant to the notification
// category it fans out to. Kept here (rather than in internal/inbox, a
// leaf package with no notify-vocabulary dependency) so inbox stays
// decoupled from the #1412 category model — see internal/notifyroute's
// router, which is the only caller.
var categoryByKind = map[string]string{
	"waitpoint":            CategoryApprovals,
	"escalation":           CategoryEscalations,
	"failed_run":           CategoryRunsFailed,
	"message":              CategoryChatReplies,
	"memory_consolidation": CategoryMemory,
}

// CategoryForKind resolves an inbox kind to its notification category.
// Returns "" for a kind with no mapping (nothing to route externally —
// still lands in the in-product inbox as before).
func CategoryForKind(kind string) string {
	return categoryByKind[kind]
}

// PriorityRank orders inbox/channel priority levels low→urgent so the
// router can compare an item's priority against a channel's min_priority
// floor. Unknown values rank as "low" (never silently over-deliver past an
// admin's floor because of a typo'd priority).
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
