import { ChatPageClient } from "./chat-page-client"

// Static export placeholder; the real slug resolves on the client via useParams.
export function generateStaticParams() {
  return [{ agentSlug: "_" }]
}

export default function ChatPage() {
  return <ChatPageClient />
}
