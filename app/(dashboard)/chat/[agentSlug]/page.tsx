import { Suspense } from "react"

import { ChatClient } from "../chat-client"

/**
 * `/chat/<agentSlug>` — the same surface, with an agent named by the path.
 *
 * The route exists for the links that already point at it, and there are many:
 * `internal/chatnotify/notify.go` emits `/chat/<slug>?session=<id>` for every
 * "your agent replied" notification, `crewship open` builds it, and crews /
 * dashboard / routines all link to it. It renders the same client as `/chat`;
 * the slug is a seed for the selection, not a different page.
 *
 * The slug is NOT read from `params`. `generateStaticParams` emits one
 * placeholder and internal/api/static.go rewrites every real slug onto that
 * one file, so `params.agentSlug` is always `"_"` on a served build — the
 * client parses `window.location.pathname` instead.
 */
export function generateStaticParams() {
  return [{ agentSlug: "_" }]
}

// Next 15 requires useSearchParams to sit inside a Suspense boundary when the
// page is statically generated. Without it the client component throws on
// render and the page comes up blank.
export default function AgentChatPage() {
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
