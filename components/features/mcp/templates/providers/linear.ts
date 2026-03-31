import type { MCPTemplate } from "../../types"

export const linear: MCPTemplate = {
  name: "linear",
  label: "Linear",
  icon: "list-checks",
  transport: "streamable-http",
  url: "https://mcp.linear.app/mcp",
  envHint: "LINEAR_API_KEY",
  headerHint: "Authorization: Bearer ${LINEAR_API_KEY}",
  oauthProvider: "linear",
}
