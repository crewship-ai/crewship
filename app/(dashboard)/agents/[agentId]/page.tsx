import { AgentOverviewPageClient } from "./overview-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function AgentOverviewPage() {
  return <AgentOverviewPageClient />
}
