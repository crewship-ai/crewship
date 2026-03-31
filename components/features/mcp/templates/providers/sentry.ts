import type { MCPTemplate } from "../../types"

export const sentry: MCPTemplate = {
  name: "sentry",
  label: "Sentry",
  icon: "bug",
  transport: "streamable-http",
  url: "https://mcp.sentry.dev/sse",
  envHint: "SENTRY_AUTH_TOKEN",
  headerHint: "Authorization: Bearer ${SENTRY_AUTH_TOKEN}",
}
