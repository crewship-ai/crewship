import { SessionsPageClient } from "./chats-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SessionsPage() {
  return <SessionsPageClient />
}
