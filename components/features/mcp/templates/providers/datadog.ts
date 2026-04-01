import type { MCPTemplate } from "../../types"

export const datadog: MCPTemplate = {
  name: "datadog",
  label: "Datadog",
  icon: "datadog",
  transport: "stdio",
  command: "npx",
  args: "-y @datadog/mcp-server",
  envHint: "DD_API_KEY,DD_APP_KEY",
}
