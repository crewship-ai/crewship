import type { MCPTemplate } from "../../types"

export const slack: MCPTemplate = {
  name: "slack",
  label: "Slack",
  icon: "slack",
  transport: "stdio",
  command: "npx",
  args: "-y @anthropic-ai/slack-mcp",
  envHint: "SLACK_TOKEN",
  oauthProvider: "slack",
}
