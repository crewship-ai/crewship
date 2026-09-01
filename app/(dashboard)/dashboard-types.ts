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

export type DashboardWindow = "24h" | "7d" | "30d"

export interface RunInsightCategory {
  key: string
  total: number
  failed: number
}

export interface RunInsightsResponse {
  window: DashboardWindow
  totals: {
    total: number
    succeeded: number
    failed: number
    running: number
  }
  duration: {
    p50_ms: number
    p95_ms: number
  }
  by_trigger: RunInsightCategory[]
  by_model: RunInsightCategory[]
  by_crew: Array<{ id: string; name: string; total: number; failed: number }>
  top_agents: Array<{ id: string; name: string; crew_name: string; total: number; failed: number }>
  truncated: boolean
}

export interface RuntimeCapacityResponse {
  enabled: boolean
  limits?: {
    MaxConcurrentStarts?: number
    MinStartInterval?: number
    RequiredFreeMB?: number
    MaxPressurePct?: number
  }
  in_flight_starts?: number
  held: Array<{
    crew_id: string
    crew_slug?: string
    reason: string
    detail?: string
    since: string
    waited_ms: number
  }>
  held_total?: number
  host_signal_available?: boolean
  host_signal_error?: string
  host?: {
    AvailableMB?: number
    TotalMB?: number
    SomeStallPct?: number
  }
}

export interface MemoryHealthResponse {
  workspace_id: string
  crew_id: string
  computed_at: string
  overall: number
  metrics: {
    freshness: number
    coverage: number
    coherence: number
    efficiency: number
    reachability: number
  }
  details: Record<string, unknown>
}

export interface CrewSpendRow {
  crew_id: string
  cost_usd: number
  call_count: number
  input_tokens: number
  output_tokens: number
}

export interface CrewSpendResponse {
  rows: CrewSpendRow[]
  since: string
  until: string
}

export interface CrewServiceSummary {
  total: number
  running: number
  degraded: number
  checked: boolean
}
