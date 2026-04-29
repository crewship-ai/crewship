// API response shapes consumed by the dashboard page.

export interface AgentSummary {
  id: string
  name: string
  slug: string
  role_title: string | null
  agent_role: string
  status: string
  crew: { name: string; slug: string; color: string | null } | null
  crew_id?: string | null
  _count: { skills: number; credentials: number; chats: number }
}

export interface CrewSummary {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
}

export interface ProjectSummary {
  id: string
  name: string
  color: string
  issue_count: number
  progress: number
}

export interface RunEntry {
  id: string
  agent_id: string
  status: string
  started_at: string | null
  finished_at: string | null
  created_at: string
}

export interface RunsResponse {
  data: RunEntry[]
  stats: { running: number; today: number; failed: number }
}

export interface MissionMetricsResponse {
  active_missions: number
  total_missions: number
  completed_24h?: number
  failed_24h?: number
  total_cost_24h: number
}

export interface KeeperRequest {
  id: string
  agent_name: string
  credential_name: string
  decision: string | null
  created_at: string
}

export interface TimeseriesBucket {
  ts: string
  series: Record<string, number>
}
export interface TimeseriesResponse {
  metric: string
  window: string
  bucket: string
  group_by: string
  buckets: TimeseriesBucket[]
  series_labels: Record<string, string>
}
