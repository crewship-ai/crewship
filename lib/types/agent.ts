/** Chat session returned by the agent chats API. */
export interface Session {
  id: string
  title: string | null
  mode: string
  status: string
  message_count: number
  started_at: string
  ended_at: string | null
}

/** Single execution run of an agent. */
export interface AgentRun {
  id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
}

/** Audit-trail event for agent configuration changes. */
export interface AuditEvent {
  id: string
  action: string
  entity_type: string
  entity_id: string
  changes: Record<string, { old?: string; new?: string }> | null
  user_name: string | null
  created_at: string
}

/** Structured log entry from crewshipd service logs. */
export interface ServiceLogEntry {
  time: string
  level: string
  msg: string
  attrs?: Record<string, string>
}

/** Structured log entry from agent-level logs. */
export interface AgentLogEntry {
  ts: string
  level: string
  agent: string
  event: string
  content?: string
  metadata?: Record<string, unknown>
}

/** Aggregated debug/diagnostics payload for an agent. */
export interface DebugData {
  agent: {
    id: string
    name: string
    cli_adapter: string
    db_status: string
  }
  crewshipd_reachable: boolean
  crewshipd: {
    status?: string
    uptime?: string
    uptime_secs?: number
    connections?: number
    started_at?: string
    providers?: Record<string, string>
    container_available?: boolean
    storage_available?: boolean
    state_available?: boolean
    llm_proxy_enabled?: boolean
    config?: Record<string, unknown>
    error?: string
  }
  runtime: {
    agent_id?: string
    status: string
    started_at?: string
    container_id?: string
    exec_id?: string
    last_activity?: string
    credential_id?: string
    session_id?: string
  }
  service_logs: ServiceLogEntry[]
  agent_logs: AgentLogEntry[]
}

/** File/directory entry returned by the agent file-browser API. */
export interface FileEntry {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
}

/** Recursive tree node used by the file explorer sidebar. */
export interface TreeNode {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
  children: TreeNode[]
  childrenLoaded?: boolean
}
