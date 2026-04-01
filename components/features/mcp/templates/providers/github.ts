import type { MCPTemplate } from "../../types"

export const github: MCPTemplate = {
  name: "github",
  label: "GitHub",
  icon: "github",
  transport: "streamable-http",
  url: "https://api.githubcopilot.com/mcp/",
  envHint: "GITHUB_PERSONAL_ACCESS_TOKEN",
  headerHint: "Authorization: Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}",
}
