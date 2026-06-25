// Shared types for the Composio integrations surface
// (`components/features/integrations/composio-integrations.tsx` + the tab
// sub-components in this folder). Mirrors the JSON shapes served by the
// /api/v1/integrations/composio/* handlers (internal/api/composio_handler.go)
// and the agents list (internal/api/agents_query.go).

export type Toolkit = { slug: string; logo?: string }

export type AuthConfig = { id: string; name: string; status: string; toolkit: Toolkit }

export type ConnectedAccount = {
  id: string
  user_id: string
  status: string
  toolkit: Toolkit
}

export type UserInventory = {
  user_id: string
  connected_accounts: ConnectedAccount[]
}

export type Inventory = {
  enabled: boolean
  auth_configs: AuthConfig[]
  users: UserInventory[]
}

export type ToolkitInfo = {
  slug: string
  name: string
  meta: {
    description?: string
    logo?: string
    tools_count?: number
    categories?: { name: string }[]
  }
}

export type ToolkitsResp = { enabled: boolean; total: number; toolkits: ToolkitInfo[] }

export type ComposioSettings = {
  configured: boolean
  source: string
  label?: string
  base_url?: string
}

// Agent (subset of /api/v1/agents) — only the fields the integrations surface
// renders. crew is nested per the agents handler response.
export type AgentLite = {
  id: string
  name: string
  slug: string
  crew?: { name: string } | null
}

// One Composio binding on an agent (GET .../agents/{id}/bind → {bindings:[…]}).
export type AgentBinding = { user_id: string; endpoint: string }

// agentId → its Composio bindings.
export type AgentBindingsMap = Record<string, AgentBinding[]>

export type Tool = {
  slug: string
  name: string
  description: string
  toolkit: Toolkit
}

export type ToolsResp = { enabled: boolean; total: number; tools: Tool[] }

export type TriggerType = {
  slug: string
  name: string
  description: string
  type: string
  toolkit: Toolkit
}

export type TriggerTypesResp = { enabled: boolean; total: number; triggers: TriggerType[] }

export type TriggerInstance = {
  id: string
  trigger_name: string
  user_id: string
  connected_account_id: string
  disabled_at?: string
}

export type ActiveTriggersResp = { enabled: boolean; triggers: TriggerInstance[] }

export type TabKey =
  | "catalog"
  | "accounts"
  | "agents"
  | "tools"
  | "triggers"
  | "mcp"
