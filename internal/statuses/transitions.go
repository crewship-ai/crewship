// Package statuses provides canonical status transition rules for issues,
// missions and tasks. Every component that validates status changes should
// reference these maps instead of maintaining its own copy.
package statuses

// ValidIssueTransitions defines allowed status transitions for issues.
// Issue statuses are a superset of the existing mission statuses.
// DUPLICATE mirrors CANCELLED's reachability: both are ways to close an
// issue without shipping it, so any status that can be cancelled can also
// be marked a duplicate. DUPLICATE itself has no outgoing transitions — it
// is a deliberate terminal sink (see docs/api-reference/issues.mdx's Issue
// Statuses table: "no transitions out"). A reopen path (DUPLICATE → BACKLOG)
// was considered but is a separate product decision, not shipped here.
var ValidIssueTransitions = map[string][]string{
	"BACKLOG":     {"TODO", "IN_PROGRESS", "CANCELLED", "DUPLICATE"},
	"TODO":        {"IN_PROGRESS", "BACKLOG", "CANCELLED", "DUPLICATE"},
	"IN_PROGRESS": {"REVIEW", "DONE", "FAILED", "CANCELLED", "TODO", "DUPLICATE"},
	"REVIEW":      {"DONE", "TODO", "IN_PROGRESS", "FAILED", "CANCELLED", "DUPLICATE"},
	"DONE":        {"BACKLOG"},
	"FAILED":      {"BACKLOG", "TODO", "IN_PROGRESS"},
	"CANCELLED":   {"BACKLOG", "TODO"},
	"DUPLICATE":   {},
}

// ValidMissionTransitions defines allowed status transitions for missions.
// Includes both mission-engine states (PLANNING, IN_PROGRESS, REVIEW, …) and
// issue tracker states so that the same map covers internal + external
// updates.
//
// B13 (#2370, PRD-ISSUES-AND-ROUTINES-2026 §3.1): REVIEW's terminal-approval
// target is DONE, not COMPLETED. The two used to be separate words for the
// identical "an operator approved this out of review" action — one on the
// issue tracker's Review handler, one on the generic mission PATCH handler —
// sharing this one column. COMPLETED is retired here; the mission PATCH
// handler (internal/api/mission_handler_mutate.go) still ACCEPTS a
// "COMPLETED" input and normalizes it to DONE before it ever reaches
// IsValidTransition, so an old client is not broken by this table changing.
var ValidMissionTransitions = map[string][]string{
	"PLANNING":    {"IN_PROGRESS", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "FAILED", "CANCELLED"},
	"REVIEW":      {"DONE", "IN_PROGRESS", "FAILED", "CANCELLED"},
	// Issue tracker statuses (invisible to MissionEngine).
	"BACKLOG": {"TODO", "IN_PROGRESS", "CANCELLED"},
	"TODO":    {"BACKLOG", "IN_PROGRESS", "CANCELLED"},
	"DONE":    {"BACKLOG"},
	"FAILED":  {"BACKLOG", "TODO", "IN_PROGRESS"},
}

// ValidTaskTransitions defines allowed status transitions for tasks.
// AWAITING_APPROVAL is intentionally excluded — it transitions only via
// the dedicated /approve endpoint.
var ValidTaskTransitions = map[string][]string{
	"PENDING":     {"IN_PROGRESS", "SKIPPED"},
	"BLOCKED":     {"PENDING", "SKIPPED"},
	"IN_PROGRESS": {"COMPLETED", "FAILED", "SKIPPED"},
}

// IsValidTransition checks whether moving from current to target is allowed
// according to the given transition map.
func IsValidTransition(transitions map[string][]string, current, target string) bool {
	for _, s := range transitions[current] {
		if s == target {
			return true
		}
	}
	return false
}
