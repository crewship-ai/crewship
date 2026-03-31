import type { MCPTemplate } from "../../types"

export const linear: MCPTemplate = {
  name: "linear",
  label: "Linear",
  icon: "list-checks",
  transport: "stdio",
  command: "npx",
  args: "-y linear-mcp",
  envHint: "LINEAR_API_KEY",
}
