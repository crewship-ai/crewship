import { Suspense } from "react"
import { ChatPageClient } from "./chat-page-client"

// Static export placeholder; the real slug resolves on the client via useParams.
export function generateStaticParams() {
  return [{ agentSlug: "_" }]
}

// Next.js 15 requires hooks like useSearchParams to live inside a
// Suspense boundary when the page is statically generated. Without it
// the client component throws on render and the whole page renders blank.
export default function ChatPage() {
  return (
    <Suspense fallback={<div className="h-full grid place-items-center text-xs text-muted-foreground">Loading chat…</div>}>
      <ChatPageClient />
    </Suspense>
  )
}
