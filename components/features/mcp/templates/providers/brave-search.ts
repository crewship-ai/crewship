import type { MCPTemplate } from "../../types"

export const braveSearch: MCPTemplate = {
  name: "brave-search",
  label: "Brave Search",
  icon: "search",
  transport: "stdio",
  command: "npx",
  args: "-y @anthropic-ai/brave-search-mcp",
  envHint: "BRAVE_API_KEY",
}
