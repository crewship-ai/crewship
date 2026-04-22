import { ToolsPageClient } from "@/components/features/agents/tools/tools-page-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function ToolsPage() {
  return <ToolsPageClient />
}
