export interface Stats {
  workspaces: number
  users: number
  agents: number
  running: number
}

export interface AdminOrg {
  id: string
  name: string
  slug: string
  created_at: string
  _count_members: number
  _count_agents: number
  _count_crews: number
}

export interface AdminUser {
  id: string
  email: string
  full_name: string | null
  created_at: string
  workspace: { id: string; name: string } | null
  role: string | null
}

export interface KeeperStatus {
  enabled: boolean
  ollama_url: string
  model: string
  ollama_online: boolean
  gatekeeper_configured: boolean
  total_requests: number
  allow_count: number
  deny_count: number
  escalate_count: number
}

export interface KeeperLogEntry {
  id: string
  agent_id: string
  agent_name: string
  crew_id: string
  credential_id: string
  credential_name: string
  intent: string
  request_type: string
  command: string | null
  decision: string | null
  reason: string | null
  risk_score: number | null
  exit_code: number | null
  ollama_prompt: string | null
  ollama_raw_response: string | null
  created_at: string
  decided_at: string | null
}

export type TabKey =
  | "overview" | "logs" | "workspaces" | "users"
  | "providers" | "resources" | "networking" | "backups"
  | "gateway" | "security" | "auth" | "flags" | "ratelimits"
