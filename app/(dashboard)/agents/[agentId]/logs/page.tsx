import { LogsPageClient } from "./logs-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function LogsPage() {
  return <LogsPageClient />
}
