import type { MCPTemplate } from "../../types"

export const notion: MCPTemplate = {
  name: "notion",
  label: "Notion",
  icon: "notion",
  transport: "stdio",
  command: "npx",
  args: "-y @notionhq/notion-mcp-server",
  envHint: "OPENAPI_MCP_HEADERS",
  oauthProvider: "notion",
}
