// ---------------------------------------------------------------------------
// MCP Config Editor — shared types
// ---------------------------------------------------------------------------

/** stdio-based MCP server (local process). */
export interface StdioServer {
  command: string
  args?: string[]
  env?: Record<string, string>
}

/** HTTP-based MCP server (remote endpoint). */
export interface HttpServer {
  type: "http"
  url: string
  headers?: Record<string, string>
  env?: Record<string, string>
}

export type MCPServer = StdioServer | HttpServer

export interface MCPConfig {
  mcpServers: Record<string, MCPServer>
}

/** Internal form-state representation of a single MCP server. */
export interface ServerEntry {
  /** Unique key for React list rendering; NOT the server name. */
  _key: number
  /** Database ID from crew_mcp_servers table (undefined for new entries). */
  id?: string
  name: string
  transport: "stdio" | "http"
  command: string
  args: string
  url: string
  headers: { key: string; value: string }[]
  env: { key: string; value: string }[]
}

export interface Credential {
  id: string
  name: string
  type: "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"
  provider?: string
  status?: "ACTIVE" | "EXPIRED" | "RATE_LIMITED" | "REVOKED" | "ERROR" | "PENDING"
}

export interface OAuthProvider {
  auth_url: string
  token_url: string
  default_scopes: string
}

/** Template definition for quick-adding popular MCP servers. */
export interface MCPTemplate {
  name: string
  label: string
  icon: string
  transport: "stdio" | "http"
  command?: string
  args?: string
  url?: string
  headerHint?: string
  envHint?: string
  oauthProvider?: string
}

export interface MCPConfigEditorProps {
  value: string
  onChange: (json: string) => void
  readOnly?: boolean
  label?: string
  workspaceId?: string
}
