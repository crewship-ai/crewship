import { HistoryPageClient } from "./history-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function HistoryPage() {
  return <HistoryPageClient />
}
