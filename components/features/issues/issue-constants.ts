import type { MissionStatus, IssuePriority, RelationType, ProjectStatus } from "@/lib/types/mission"

export const ISSUE_STATUSES: MissionStatus[] = [
  "BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "CANCELLED",
]

export const ALL_PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

export const RELATION_TYPE_LABELS: Record<RelationType, string> = {
  blocks: "Blocks",
  blocked_by: "Blocked by",
  relates_to: "Related",
  duplicate_of: "Duplicate of",
}

export const RELATION_TYPE_OPTIONS: { value: RelationType; label: string }[] = [
  { value: "relates_to", label: "Related to" },
  { value: "blocks", label: "Blocks" },
  { value: "blocked_by", label: "Blocked by" },
  { value: "duplicate_of", label: "Duplicate of" },
]

export const PROJECT_STATUSES: { value: ProjectStatus; label: string }[] = [
  { value: "backlog", label: "Backlog" },
  { value: "planned", label: "Planned" },
  { value: "in_progress", label: "In Progress" },
  { value: "paused", label: "Paused" },
  { value: "completed", label: "Completed" },
  { value: "cancelled", label: "Cancelled" },
]

export const HEALTH_OPTIONS: { value: string; label: string; color: string }[] = [
  { value: "on_track", label: "On Track", color: "text-green-400" },
  { value: "at_risk", label: "At Risk", color: "text-yellow-400" },
  { value: "off_track", label: "Off Track", color: "text-red-400" },
]

export const PRIORITY_OPTIONS: { value: string; label: string }[] = [
  { value: "urgent", label: "Urgent" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "none", label: "No priority" },
]
