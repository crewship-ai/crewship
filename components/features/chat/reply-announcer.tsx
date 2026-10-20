"use client"

import { useEffect, useRef, useState } from "react"
import type { ChatTurn } from "@/hooks/use-chat"

const ENDED: Partial<Record<NonNullable<ChatTurn["turnState"]>, string>> = {
  cancelled: "Reply stopped.",
  interrupted: "Reply interrupted.",
  failed: "Reply failed.",
}

/**
 * The transcript is a log with live announcements off, so a screen reader is
 * never read a streamed reply token by token. This one polite region says the
 * thing worth hearing instead: that a reply the person watched has ended, and
 * how. History that loads already finished is not announced.
 */
export function ReplyAnnouncer({ isStreaming, turns, agentName, sessionId }: {
  isStreaming: boolean
  turns: Pick<ChatTurn, "role" | "turnState">[]
  agentName?: string
  sessionId?: string
}) {
  const [message, setMessage] = useState("")
  const watched = useRef<{ session?: string } | null>(null)
  const last = turns.findLast(turn => turn.role === "assistant")
  // Read when the stream ends, not tracked: a later re-render (a reaction, a
  // card) must neither repeat nor clear the announcement.
  const outcome = useRef("")
  outcome.current = !last ? "" : (last.turnState && ENDED[last.turnState]) || `${agentName ?? "Agent"} replied.`
  useEffect(() => {
    if (isStreaming) { watched.current = { session: sessionId }; return }
    const was = watched.current
    watched.current = null
    setMessage(was && was.session === sessionId ? outcome.current : "")
  }, [isStreaming, sessionId])
  return <div role="status" aria-live="polite" className="sr-only">{message}</div>
}
