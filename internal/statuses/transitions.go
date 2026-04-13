// Package statuses provides canonical status transition rules for issues,
// missions and tasks. Every component that validates status changes should
// reference these maps instead of maintaining its own copy.
package statuses

// ValidIssueTransitions defines allowed status transitions for issues.
// Issue statuses are a superset of the existing mission statuses.
var ValidIssueTransitions = map[string][]string{
	"BACKLOG":     {"TODO", "IN_PROGRESS", "CANCELLED"},
	"TODO":        {"IN_PROGRESS", "BACKLOG", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "DONE", "FAILED", "CANCELLED", "TODO"},
	"REVIEW":      {"DONE", "TODO", "IN_PROGRESS", "FAILED", "CANCELLED"},
	"DONE":        {"BACKLOG"},
	"FAILED":      {"BACKLOG", "TODO", "IN_PROGRESS"},
	"CANCELLED":   {"BACKLOG", "TODO"},
	"DUPLICATE":   {},
}

// ValidMissionTransitions defines allowed status transitions for missions.
// Includes both mission-engine states (PLANNING, COMPLETED, …) and issue
// tracker states so that the same map covers internal + external updates.
var ValidMissionTransitions = map[string][]string{
	"PLANNING":    {"IN_PROGRESS", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "FAILED", "CANCELLED"},
	"REVIEW":      {"COMPLETED", "IN_PROGRESS", "FAILED", "CANCELLED"},
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
