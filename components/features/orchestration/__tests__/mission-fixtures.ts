import type { Mission, MissionTask } from "@/lib/types/mission"

export const now = new Date(2026, 9, 3, 12, 0, 0)
export const ago = (seconds: number) => new Date(now.getTime() - seconds * 1000).toISOString()
export function task(id: string, overrides: Partial<MissionTask> = {}): MissionTask {
  return {
    id, mission_id: "m", assigned_agent_id: null, agent_name: null, agent_slug: "sam",
    title: `Task ${id}`, description: null, status: "IN_PROGRESS", task_order: 0, depends_on: "[]",
    iteration: null, max_iterations: null, result_summary: null, output_path: null, error_message: null,
    assignment_id: null, token_count: null, estimated_cost: null, started_at: null, completed_at: null,
    duration_ms: null, created_at: ago(10), updated_at: ago(10), complexity: null, token_budget: null,
    tokens_used: null, tool_calls_count: null, tool_calls_budget: null, confidence: null,
    approval_required: false, approval_status: null, approved_by: null, approved_at: null,
    needs_review: false, handoff_context: null, evaluation_status: null, evaluation_notes: null,
    retry_count: null, priority: null, labels: null, ...overrides,
  }
}
export function mission(id: string, overrides: Partial<Mission> = {}): Mission {
  return {
    id, workspace_id: "workspace", crew_id: "crew", lead_agent_id: "alex", lead_agent_name: "Alex",
    lead_agent_slug: "alex", trace_id: "trace", title: `Mission ${id}`, description: null,
    status: "IN_PROGRESS", plan: null, workflow_template: null, total_token_count: null,
    total_estimated_cost: null, created_at: ago(20), updated_at: ago(20), completed_at: null,
    task_stats: null, tasks: [], total_token_budget: null, complexity: null, pattern: null, ...overrides,
  }
}
