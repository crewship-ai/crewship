import { LogsPageClient } from "@/components/features/agents/logs/logs-page-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function LogsPage() {
  return <LogsPageClient />
}
