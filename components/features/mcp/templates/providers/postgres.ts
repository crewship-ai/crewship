import type { MCPTemplate } from "../../types"

export const postgres: MCPTemplate = {
  name: "postgres",
  label: "PostgreSQL",
  icon: "database",
  transport: "stdio",
  command: "npx",
  args: "-y @modelcontextprotocol/server-postgres",
  envHint: "DATABASE_URL",
}
