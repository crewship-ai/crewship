export interface Assignment {
  id: string
  task: string
  status: "PENDING" | "RUNNING" | "COMPLETED" | "FAILED"
  assigned_by_name: string
  assigned_by_slug: string
  assigned_to_name: string
  assigned_to_slug: string
  result_summary: string | null
  error_message: string | null
  started_at: string | null
  finished_at: string | null
  created_at: string
}
