import type { MCPTemplate } from "../../types"

export const googleWorkspace: MCPTemplate = {
  name: "google-workspace",
  label: "Google Workspace",
  icon: "mail",
  transport: "stdio",
  command: "npx",
  args: "-y @dguido/google-workspace-mcp",
  envHint: "GOOGLE_ACCESS_TOKEN",
  oauthProvider: "google",
}
