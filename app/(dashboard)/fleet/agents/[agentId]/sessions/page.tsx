import { SessionsPageClient } from "../chats/chats-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SessionsPage() {
  return <SessionsPageClient />
}
