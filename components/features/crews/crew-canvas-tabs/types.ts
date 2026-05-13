export interface AgentSummary {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  avatar_seed?: string | null
  avatar_style?: string | null
  llm_provider?: string | null
  llm_model?: string | null
  _count?: { skills: number; credentials: number }
}

export interface CrewRecord {
  id: string
  workspace_id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  avatar_style: string | null
  issue_prefix: string | null
  network_mode: string
  allowed_domains: string[] | string | null
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  runtime_image: string | null
  devcontainer_config: string | null
  mise_config: string | null
  escalation_config: string | null
  cached_image: string | null
  created_at: string
  updated_at: string
  _count?: { agents: number; members: number }
}

export interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  created_at: string
}

export interface IssuesSnapshot {
  Backlog: number
  Todo: number
  InProgress: number
  InReview: number
  Done: number
}

export interface IssueRow {
  id: string
  identifier: string | null
  title: string
  status: string
  created_at?: string
}

export interface CrewIntegration {
  id: string
  integration_id: string
  name: string
  type: string
  status: string
}

export interface MemberUser {
  id: string
  email: string
  full_name: string | null
  avatar_url: string | null
}

export interface CrewMemberRow {
  id: string
  crew_id: string
  user_id: string
  created_at: string
  user?: MemberUser | null
}

export function formatMemory(mb: number): string {
  if (!Number.isFinite(mb) || mb <= 0) return "—"
  if (mb < 1024) return `${mb} MB`
  const gb = mb / 1024
  return gb >= 10 ? `${gb.toFixed(0)} GB` : `${gb.toFixed(1)} GB`
}

export function issueStatusColor(status: string | undefined): string {
  const s = (status ?? "").toLowerCase()
  if (s.includes("progress")) return "bg-blue-400"
  if (s.includes("review")) return "bg-amber-400"
  if (s.includes("done") || s.includes("closed") || s.includes("complete")) return "bg-emerald-400"
  if (s.includes("blocked") || s.includes("error") || s.includes("cancel")) return "bg-red-500"
  if (s.includes("todo")) return "bg-zinc-400"
  return "bg-zinc-600"
}
