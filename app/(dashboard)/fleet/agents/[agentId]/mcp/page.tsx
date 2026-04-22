import { MCPPageClient } from "./mcp-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function MCPPage() {
  return <MCPPageClient />
}
