import { Suspense } from "react"

import { ChatClient } from "./chat-client"

/**
 * `/chat` — the chat surface, with no agent named.
 *
 * Opens on the conversation you were last in rather than on an index of
 * agents. The index this replaced listed threads on the right and a roster on
 * the left, which meant arriving at chat took two decisions before you could
 * read anything: which agent, then which conversation. The agent is an
 * attribute of the conversation, not a step before it.
 */
export default function ChatPage() {
  return (
    <Suspense
      fallback={
        <div className="grid h-full place-items-center text-label text-muted-foreground">
          Loading chat…
        </div>
      }
    >
      <ChatClient />
    </Suspense>
  )
}
