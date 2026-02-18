import { Suspense } from "react"
import { ChatPageClient } from "./chat-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function ChatPage() {
  return (
    <Suspense>
      <ChatPageClient />
    </Suspense>
  )
}
