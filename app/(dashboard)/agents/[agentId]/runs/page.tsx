import { RunsPageClient } from "./runs-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function RunsPage() {
  return <RunsPageClient />
}
