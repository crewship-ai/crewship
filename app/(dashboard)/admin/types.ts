/** High-level platform statistics shown on the admin overview dashboard. */
export interface Stats {
  workspaces: number
  users: number
  /** What the licence caps first, so the overview reads it against max_crews. */
  crews?: number
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
  /** Same-origin authed endpoint with a ?v= cache stamp. Returned by
   *  /api/v1/admin/users since it was written; the type simply never
   *  declared it, so no admin surface could draw a face. */
  avatar_url?: string | null
  created_at: string
  workspace: { id: string; name: string } | null
  role: string | null
}

/** Live health probe for the overview status dots — GET /api/v1/admin/health. */
export interface AdminHealth {
  uptime_seconds: number
  db?: { connected: boolean; error?: string }
  disk?: { path?: string; error?: string; free_bytes?: number; total_bytes?: number; used_pct?: number }
  /** Live level, the configured baseline, and the expiry of a timed override. */
  log_level?: { level: string; baseline?: string; expires_at?: string | null }
  /** Where ENCRYPTION_KEY came from. "generated" means the key file sits
   *  beside the database, so a disk copy carries both it and the ciphertext. */
  encryption_key_source?: string
}

/** GET /api/v1/system/version — build identity plus the update check. */
export interface VersionInfo {
  /** Build identity (dev builds report "dev" as current). */
  commit?: string
  build_time?: string
  go_version?: string
  os?: string
  arch?: string
  dirty?: boolean
  schema_version?: number
  current: string
  latest?: string | null
  newer?: boolean
  url?: string | null
}

/** One derived warning from GET /api/v1/admin/security-posture. */
export interface PostureWarning {
  key: string
  severity: string
  message: string
}

/** GET /api/v1/admin/security-posture — the instance's own read of itself. */
export interface SecurityPosture {
  environment?: string
  warnings: PostureWarning[]
}

/** GET /api/v1/admin/journal/verify — the tamper-evident chain's own verdict. */
export interface JournalIntegrity {
  ok?: boolean
  valid?: boolean
  entries_verified?: number
  entries?: number
  /** What the server actually sends (internal/api journal verify). */
  count?: number
  checkpoints?: number
  error?: string
}

/** License edition + limits — GET /api/v1/system/license (read-only). */
export interface LicenseInfo {
  edition: string
  licensee_org?: string
  max_crews: number
  max_agents_per_crew: number
  max_members: number
  features: string[]
}

/** Crash/usage telemetry consent — GET /api/v1/system/telemetry (read-only; flipped via CLI). */
export interface TelemetryInfo {
  enabled: boolean
  install_id?: string
}

/** Runtime status of the Keeper (Ollama-based credential gatekeeper) subsystem. */
export interface KeeperStatus {
  enabled: boolean
  ollama_url: string
  model: string
  ollama_online: boolean
  /** Whether the endpoint was actually dialled. false + ollama_online false is
   *  "not checked", which is a different thing to say than "offline". */
  ollama_probed?: boolean
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
  | "notifications"
  | "ratelimits"

/** GET /api/v1/crewshipd — the host daemon agents talk to. */
export interface DaemonStatus {
  status: string
  connections: number
  /** Go duration string, e.g. "5m53.3s". */
  uptime: string
}

/** GET /api/v1/system/aux-status — the helper models behind the product. */
export interface AuxSubsystem {
  id: string
  label: string
  provider?: string
  model?: string
  healthy?: boolean
  reachable?: boolean
  reach_detail?: string
}
export interface AuxStatus {
  subsystems: AuxSubsystem[]
}

/** GET /api/v1/agents/crews-status — every agent's state, counted. */
export interface AgentsStatus {
  total: number
  running: number
  error: number
  idle: number
  queued: number
}

/** GET /api/v1/admin/keeper/health — the credential judge's recent verdicts. */
export interface KeeperHealth {
  samples: number
  allow: number
  deny: number
  escalate: number
  judge_failures: number
  p95_latency_ms: number
  min_samples: number
}

/** One bucket of GET /api/v1/metrics/timeseries. */
export interface TimeseriesPoint {
  ts: string
  value: number
}
