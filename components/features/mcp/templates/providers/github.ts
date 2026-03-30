import type { MCPTemplate } from "../../types"

export const github: MCPTemplate = {
  name: "github",
  label: "GitHub",
  icon: "github",
  transport: "stdio",
  command: "npx",
  args: "-y @modelcontextprotocol/server-github",
  envHint: "GITHUB_TOKEN",
}
