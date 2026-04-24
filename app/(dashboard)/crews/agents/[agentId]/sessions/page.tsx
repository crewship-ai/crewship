import { SessionsPageClient } from "@/components/features/agents/sessions/sessions-page-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SessionsPage() {
  return <SessionsPageClient />
}
