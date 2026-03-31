import type { MCPTemplate } from "../../types"

export const googleWorkspace: MCPTemplate = {
  name: "google-workspace",
  label: "Google Workspace",
  icon: "mail",
  transport: "stdio",
  command: "npx",
  args: "-y @anthropic-ai/google-workspace-mcp",
  envHint: "GOOGLE_ACCESS_TOKEN",
  oauthProvider: "google",
}
