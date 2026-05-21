/** High-level platform statistics shown on the admin overview dashboard. */
export interface Stats {
  workspaces: number
  users: number
  agents: number
  running: number
}

/** A workspace (organization) as seen in the admin panel, with member/agent/crew counts. */
export interface AdminOrg {
  id: string
  name: string
  slug: string
  created_at: string
  _count_members: number
  _count_agents: number
  _count_crews: number
}

/** A user record as displayed in the admin users table. */
export interface AdminUser {
  id: string
  email: string
  full_name: string | null
  created_at: string
  workspace: { id: string; name: string } | null
  role: string | null
}

/** Runtime status of the Keeper (Ollama-based credential gatekeeper) subsystem. */
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

/** An audit log entry from the Keeper, recording a credential access decision (allow/deny/escalate). */
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

/** Active tab identifier for the admin panel navigation.
 *  Only real, wired tabs are listed here — placeholder/stub sections
 *  were removed. Reintroduce a key when its backend lands. */
export type TabKey =
  | "overview"
  | "workspaces"
  | "users"
  | "providers"
  | "security"
  | "reviews"
  | "backups"
