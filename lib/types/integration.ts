/** A workspace-level MCP (Model Context Protocol) server configuration. */
export interface WorkspaceMCPServer {
  id: string
  workspace_id: string
  name: string
  display_name: string
  transport: "streamable-http" | "stdio"
  endpoint?: string | null
  command?: string | null
  args_json?: string | null
  env_json?: string | null
  config_json?: string | null
  icon?: string | null
  enabled: boolean
  created_at: string
  updated_at: string
  agent_binding_count: number
  crew_server_count: number
}

/** A crew-scoped MCP server, optionally linked to a workspace-level server. */
export interface CrewMCPServer {
  id: string
  crew_id: string
  workspace_mcp_server_id?: string | null
  name: string
  display_name: string
  transport: "streamable-http" | "stdio"
  endpoint?: string | null
  command?: string | null
  args_json?: string | null
  env_json?: string | null
  config_json?: string | null
  icon?: string | null
  enabled: boolean
  created_at: string
  updated_at: string
  agent_binding_count: number
}

/** Binding between an agent and an MCP server, with optional credential and config override. */
export interface AgentMCPBinding {
  id: string
  agent_id: string
  mcp_server_id: string
  mcp_server_scope: "workspace" | "crew"
  credential_id?: string | null
  cred_type?: string | null
  cred_header?: string | null
  enabled: boolean
  config_override_json?: string | null
  created_at: string
  server_name: string
  server_display_name: string
  credential_name?: string | null
}

/** A fully resolved integration combining server config and credential info, ready for agent use. */
export interface ResolvedIntegration {
  server_id: string
  scope: "workspace" | "crew"
  name: string
  display_name: string
  transport: string
  endpoint?: string | null
  command?: string | null
  args_json?: string | null
  env_json?: string | null
  config_json?: string | null
  icon?: string | null
  enabled: boolean
  credential_id?: string | null
  credential_name?: string | null
}

/** A recorded MCP tool invocation by an agent, with status and duration tracking. */
export interface MCPToolCall {
  id: string
  workspace_id: string
  crew_id?: string | null
  agent_id: string
  mcp_server_id: string
  mcp_server_scope: string
  tool_name: string
  input_hash?: string | null
  status: "success" | "error" | "denied" | "timeout"
  duration_ms?: number | null
  error_message?: string | null
  session_id?: string | null
  created_at: string
}
