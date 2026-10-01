package retention

import (
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/work"
)

// HousekeepingItem is an instance-wide limit set in code, not per workspace.
// The console shows them read-only so an administrator sees everything the
// server deletes on its own, not only what they can change.
type HousekeepingItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

func dayString(d time.Duration) string {
	n := int(d / (24 * time.Hour))
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// Housekeeping lists the fixed limits. Values come from the constants the
// sweeps use where those are exported; the rest are named with their source.
func Housekeeping() []HousekeepingItem {
	return []HousekeepingItem{
		{Key: "journal_compaction", Label: "Journal compaction", Value: "30 days",
			Detail: "Low-value journal entries older than 30 days move to the journal archive, daily at 03:00 UTC (internal/consolidate)."},
		{Key: "memory_versions_floor", Label: "Memory version history, instance-wide", Value: "30 days",
			Detail: "An instance-wide pass ages memory versions out after 30 days, keeping the latest versions of every file; a workspace window can only shorten it."},
		{Key: "routine_runs_keep_last", Label: "Routine runs always kept", Value: fmt.Sprintf("last %d per routine", pipeline.DefaultKeepLastNRunsPerPipeline),
			Detail: "The newest runs of every routine survive the routine runs window, however old."},
		{Key: "routine_webhook_receipts", Label: "Routine webhook receipts", Value: dayString(pipeline.RoutineReceiptDedupWindow),
			Detail: "The record that deduplicates a routine webhook delivery. After it expires the same delivery id counts as new work."},
		{Key: "work_delivery_receipts", Label: "Agent webhook delivery receipts", Value: dayString(work.DefaultDedupWindow),
			Detail: "Deduplication records of agent webhook deliveries, kept while their work is unfinished."},
		{Key: "work_raw_bodies", Label: "Delivered work bodies", Value: dayString(work.DefaultRawBodyRetention) + " after done",
			Detail: "The raw payload of a webhook delivery, dropped once its work has been finished this long."},
		{Key: "page_project_history", Label: "Page project builds and revisions", Value: "hourly",
			Detail: "Keeps the newest 64 revisions and builds and 32 publications per page (128 per workspace) plus everything live, draft or running."},
		{Key: "orphaned_attachments", Label: "Orphaned attachment files", Value: "hourly",
			Detail: "Files no attachment row names any more, and chat uploads never sent within an hour, are removed."},
		{Key: "credential_audit_unattributed", Label: "Unattributed credential reads", Value: fmt.Sprintf("%d days", DefaultCredentialAuditDays),
			Detail: "Credential audit rows that belong to no workspace."},
		{Key: "pre_migration_snapshots", Label: "Pre-upgrade database snapshots", Value: fmt.Sprintf("last %d", database.MigrationBackupRetention),
			Detail: "A copy of the database taken before each schema upgrade; older copies are removed."},
	}
}
