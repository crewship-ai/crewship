import { DebugPageClient } from "./debug-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function DebugPage() {
  return <DebugPageClient />
}
