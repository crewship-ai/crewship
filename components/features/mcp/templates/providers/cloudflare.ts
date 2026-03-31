import type { MCPTemplate } from "../../types"

export const cloudflare: MCPTemplate = {
  name: "cloudflare",
  label: "Cloudflare",
  icon: "cloud",
  transport: "streamable-http",
  url: "https://docs.mcp.cloudflare.com/mcp",
  envHint: "CLOUDFLARE_API_TOKEN",
  headerHint: "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}",
  oauthProvider: "cloudflare",
}
