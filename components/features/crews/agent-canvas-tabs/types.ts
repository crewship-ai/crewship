import type { ProviderLogin } from "@/lib/credentials/provider-logins"

/** `pays_with` on GET /agents/{id} — PRD provider-logins §10.3. Derived by the
 *  server from the agent's bindings and adapter; absent on an older server. */
export interface AgentPaysWith {
  credential_id?: string
  name?: string
  login?: ProviderLogin
  provider?: string
  restricted?: boolean
}

export interface AgentRecord {
  id: string
  workspace_id: string
  crew_id: string | null
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  lead_mode: string | null
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  cli_tools?: string[] | null
  schedule_cron?: string | null
  schedule_prompt?: string | null
  schedule_enabled?: boolean | null
  schedule_last_run?: string | null
  schedule_next_run?: string | null
  avatar_seed: string | null
  avatar_style: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
  updated_at: string
  crew: { id?: string; name: string; slug: string; color: string | null; avatar_style: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
  // PR-D F5 ephemeral lifecycle (server returns these; absent on permanent agents).
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
  parent_lead_id?: string | null
  hire_reason?: string | null
  /** The seat that pays for this agent's model, when the server reports it. */
  pays_with?: AgentPaysWith | null
}

export interface InboxSummary { count: number; summary?: string; cost?: number }

export interface ChatRow {
  id: string
  title: string | null
  message_count: number
  status: string
  started_at: string
  last_activity_at?: string | null
  ended_at: string | null
  created_at: string
}

export interface RunRow {
  id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
  created_at: string
}

export interface AgentSkillRow {
  id: string
  skill_id: string
  enabled: boolean
  skill: { id: string; name: string; slug: string; display_name?: string | null; description?: string | null; category?: string | null; icon?: string | null; version?: string | null }
}

export interface AgentCredRow {
  source?: string
  id: string
  credential_id: string
  credential_name: string
  credential_type: string
  credential_provider: string
  credential_status: string
  env_var_name: string
  priority: number
  created_at: string
}

export interface PeerMessageRow {
  id?: string
  from_agent_id?: string
  from_agent_name?: string
  from_agent_slug?: string
  preview?: string
  question?: string
  response?: string | null
  direction?: string
  to_agent_name?: string
  to_agent_slug?: string
  created_at?: string
}

export const ROLE_OPTIONS = [
  { value: "AGENT", label: "Agent" },
  { value: "LEAD", label: "Lead" },
] as const

export const TOOL_PROFILE_OPTIONS = [
  { value: "CODING", label: "Coding (full)" },
  { value: "SANDBOX", label: "Sandbox (restricted)" },
  { value: "READONLY", label: "Read-only" },
] as const
