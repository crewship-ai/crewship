import type { MCPTemplate } from "../../types"

export const supabase: MCPTemplate = {
  name: "supabase",
  label: "Supabase",
  icon: "supabase",
  transport: "stdio",
  command: "npx",
  args: "-y @supabase/mcp-server-supabase",
  envHint: "SUPABASE_ACCESS_TOKEN",
}
