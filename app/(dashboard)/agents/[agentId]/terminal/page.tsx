import { TerminalPageClient } from "./terminal-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function TerminalPage() {
  return <TerminalPageClient />
}
