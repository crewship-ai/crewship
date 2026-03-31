import type { MCPTemplate } from "../../types"

export const filesystem: MCPTemplate = {
  name: "filesystem",
  label: "Filesystem",
  icon: "folder",
  transport: "stdio",
  command: "npx",
  args: "-y @modelcontextprotocol/server-filesystem /tmp",
}
