/**
 * Shared types for the integrations admin surface
 * (`app/(dashboard)/integrations`). Lifted out of the original
 * page.tsx so the colocated sub-components can import them without
 * re-declaring the shape.
 */

export interface CrewIntegration {
  id: string
  crew_id: string
  crew_name: string
  crew_slug: string
  name: string
  display_name: string
  transport: string
  endpoint: string | null
  command: string | null
  args_json: string | null
  env_json: string | null
  icon: string | null
  enabled: boolean
  created_at: string
  updated_at: string
  agent_binding_count: number
  auth_status: "connected" | "missing" | "expired" | "none"
}

export interface AgentInfo {
  id: string
  name: string
  slug: string
}

export interface CrewInfo {
  id: string
  name: string
  slug: string
}

export interface AgentBinding {
  id: string
  mcp_server_id: string
}

export interface TestResult {
  status: "ok" | "auth_required" | "error" | "skipped"
  message?: string
}
