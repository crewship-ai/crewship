import type { MCPTemplate } from "../../types"

export const gitlab: MCPTemplate = {
  name: "gitlab",
  label: "GitLab",
  icon: "git-branch",
  transport: "streamable-http",
  url: "https://gitlab.com/api/v4/mcp",
  envHint: "GITLAB_TOKEN",
  headerHint: "Authorization: Bearer ${GITLAB_TOKEN}",
  oauthProvider: "gitlab",
}
