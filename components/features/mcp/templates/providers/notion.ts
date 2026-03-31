import type { MCPTemplate } from "../../types"

export const notion: MCPTemplate = {
  name: "notion",
  label: "Notion",
  icon: "book-open",
  transport: "stdio",
  command: "npx",
  args: "-y @notionhq/notion-mcp-server",
  envHint: "NOTION_API_KEY",
}
