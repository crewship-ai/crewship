export type MissionStatus =
  | "BACKLOG"
  | "TODO"
  | "PLANNING"
  | "IN_PROGRESS"
  | "REVIEW"
  | "COMPLETED"
  | "DONE"
  | "FAILED"
  | "CANCELLED"
  | "DUPLICATE"

export type IssuePriority = "urgent" | "high" | "medium" | "low" | "none"
export type MissionType = "issue" | "orchestration" | "scheduled" | "hired"

export type MissionTaskStatus =
  | "PENDING"
  | "BLOCKED"
  | "IN_PROGRESS"
  | "COMPLETED"
  | "FAILED"
  | "SKIPPED"
  | "AWAITING_APPROVAL"

export interface TaskStats {
  total: number
  pending: number
  blocked: number
  in_progress: number
  completed: number
  failed: number
  skipped: number
  awaiting_approval: number
}

export type TaskComplexity = "SIMPLE" | "MEDIUM" | "COMPLEX"
export type EvaluationStatus = "PENDING" | "PASSED" | "FAILED"

export interface MissionTask {
  id: string
  mission_id: string
  assigned_agent_id: string | null
  agent_name: string | null
  agent_slug: string | null
  title: string
  description: string | null
  status: MissionTaskStatus
  task_order: number
  depends_on: string
  iteration: number | null
  max_iterations: number | null
  result_summary: string | null
  output_path: string | null
  error_message: string | null
  assignment_id: string | null
  token_count: number | null
  estimated_cost: number | null
  started_at: string | null
  completed_at: string | null
  duration_ms: number | null
  created_at: string
  updated_at: string
  // Scaling & handoff fields (migration 27)
  complexity: TaskComplexity | null
  token_budget: number | null
  tokens_used: number | null
  tool_calls_count: number | null
  tool_calls_budget: number | null
  confidence: number | null
  approval_required: boolean
  approval_status: "APPROVED" | "REJECTED" | null
  approved_by: string | null
  approved_at: string | null
  needs_review: boolean
  handoff_context: string | null
  evaluation_status: EvaluationStatus | null
  evaluation_notes: string | null
  retry_count: number | null
  priority: number | null
  labels: string | null
}

export type MissionPattern = "CHAIN" | "PARALLEL" | "ORCHESTRATOR"

export interface Mission {
  id: string
  workspace_id: string
  crew_id: string
  lead_agent_id: string
  lead_agent_name: string
  lead_agent_slug: string
  trace_id: string
  title: string
  description: string | null
  status: MissionStatus
  plan: string | null
  workflow_template: string | null
  total_token_count: number | null
  total_estimated_cost: number | null
  created_at: string
  updated_at: string
  completed_at: string | null
  task_stats: TaskStats | null
  tasks: MissionTask[]
  // Scaling fields (migration 27)
  total_token_budget: number | null
  complexity: TaskComplexity | null
  pattern: MissionPattern | null
  // Issue tracker fields (migration 37)
  number?: number | null
  identifier?: string | null
  priority?: IssuePriority
  assignee_type?: "user" | "agent" | null
  assignee_id?: string | null
  assignee_name?: string | null
  due_date?: string | null
  sort_order?: number
  mission_type?: MissionType
  project_id?: string | null
  project_name?: string | null
  milestone_id?: string | null
  parent_issue_id?: string | null
  estimate?: number | null
  sub_issues_count?: number
  labels?: IssueLabel[]
  crew_name?: string
  crew_slug?: string
  comment_count?: number
}

export interface IssueLabel {
  id: string
  name: string
  color: string
  label_group: string | null
}

export type ProjectStatus = "backlog" | "planned" | "in_progress" | "paused" | "completed" | "cancelled"
export type ProjectHealth = "on_track" | "at_risk" | "off_track"

export interface Project {
  id: string
  workspace_id: string
  name: string
  slug: string
  description: string | null
  icon: string | null
  color: string
  status: ProjectStatus
  priority: IssuePriority
  health: ProjectHealth
  lead_type: "user" | "agent" | null
  lead_id: string | null
  lead_name?: string | null
  start_date: string | null
  target_date: string | null
  created_at: string
  updated_at: string
  issue_count: number
  done_count: number
  progress: number
}

export interface Milestone {
  id: string
  project_id: string
  name: string
  description: string | null
  target_date: string | null
  status: "active" | "completed" | "cancelled"
  position: number
  issue_count?: number
  done_count?: number
  created_at: string
  updated_at: string
}

export interface Notification {
  id: string
  actor_type: "user" | "agent" | "system"
  actor_id: string
  actor_name?: string
  action: string
  entity_type: string
  entity_id: string | null
  entity_title: string | null
  read_at: string | null
  created_at: string
}

export interface SavedView {
  id: string
  name: string
  filters_json: string
  sort_json: string | null
  view_type: "board" | "list"
  is_default: boolean
  shared: boolean
  created_at: string
}

export interface RecurringIssue {
  id: string
  crew_id: string
  crew_name?: string
  title: string
  description: string | null
  priority: string
  project_id: string | null
  milestone_id: string | null
  assignee_type: string | null
  assignee_id: string | null
  cron_expression: string
  enabled: boolean
  next_run: string | null
  last_run: string | null
  run_count: number
  created_at: string
}

export interface TriageRule {
  id: string
  name: string
  pattern: string
  match_type: "contains" | "regex" | "exact"
  crew_id: string | null
  assignee_id: string | null
  priority: string | null
  project_id: string | null
  labels_json: string
  position: number
  enabled: boolean
  match_count: number
  created_at: string
}

export type RelationType = "blocks" | "blocked_by" | "relates_to" | "duplicate_of"

export interface IssueRelation {
  id: string
  source_id: string
  target_id: string
  relation_type: RelationType
  target_identifier?: string
  target_title?: string
  target_status?: string
  created_at: string
}

export interface IssueActivity {
  id: string
  mission_id: string
  actor_type: "user" | "agent" | "system"
  actor_id: string
  actor_name?: string
  action: string
  details: string | null
  created_at: string
}

export interface IssueComment {
  id: string
  mission_id: string
  author_type: "user" | "agent"
  author_id: string
  author_name?: string
  body: string
  created_at: string
  updated_at: string
}
