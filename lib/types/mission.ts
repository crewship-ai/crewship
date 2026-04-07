export type MissionStatus =
  | "PLANNING"
  | "IN_PROGRESS"
  | "REVIEW"
  | "COMPLETED"
  | "FAILED"
  | "CANCELLED"

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
}
