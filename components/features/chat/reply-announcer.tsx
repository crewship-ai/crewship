"use client"

import { useEffect, useRef, useState } from "react"
import type { ChatTurn } from "@/hooks/use-chat"

const ENDED: Partial<Record<NonNullable<ChatTurn["turnState"]>, string>> = {
  cancelled: "Reply stopped.",
  interrupted: "Reply interrupted.",
  failed: "Reply failed.",
}

type AnnouncedTurn = Pick<ChatTurn, "id" | "role" | "turnState" | "isStreaming" | "authorUserId">

/** How the latest reply ended, or "" when no reply followed the last message. */
function outcomeOf(turns: AnnouncedTurn[], agent: string): string {
  const lastUser = turns.findLastIndex(turn => turn.role === "user")
  const reply = turns.slice(lastUser + 1).findLast(turn => turn.role === "assistant")
  if (!reply) return ""
  return (reply.turnState && ENDED[reply.turnState]) || `${agent} replied.`
}

/**
 * The transcript is a log with live announcements off, so a screen reader is
 * never read a streamed reply token by token. This one polite region says what
 * is worth hearing instead: that a reply the person watched has ended, and
 * how, and that a message arrived without a stream (a teammate, a reply
 * recovered after a reconnect). History that loads is not announced.
 */
export function ReplyAnnouncer({ isStreaming, turns, agentName, sessionId }: {
  isStreaming: boolean
  turns: AnnouncedTurn[]
  agentName?: string
  sessionId?: string
}) {
  const [message, setMessage] = useState({ text: "", n: 0 })
  const agent = agentName ?? "Agent"
  // Read when something happens, not tracked: an unrelated re-render must
  // neither repeat nor clear an announcement.
  const latest = useRef({ turns, agent })
  latest.current = { turns, agent }
  const watched = useRef<string | undefined | null>(null)
  const seen = useRef<{ session?: string; ids: Set<string> } | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  // Clear first, then speak: a region whose text does not change is not
  // announced again, and the second "replied" of a conversation must be.
  const say = (text: string) => {
    clearTimeout(timer.current)
    setMessage(m => ({ text: "", n: m.n }))
    timer.current = setTimeout(() => setMessage(m => ({ text, n: m.n + 1 })), 60)
  }
  const sayRef = useRef(say)
  sayRef.current = say

  useEffect(() => {
    if (isStreaming) { watched.current = sessionId; return }
    const was = watched.current
    watched.current = null
    if (was !== null && was === sessionId) {
      const text = outcomeOf(latest.current.turns, latest.current.agent)
      if (text) sayRef.current(text)
    }
  }, [isStreaming, sessionId])

  // A finished message that arrives on its own in this conversation. The first
  // list seen for a conversation is its history and is never announced.
  const ids = turns.map(turn => turn.id).join(" ")
  useEffect(() => {
    const current = latest.current.turns
    const before = seen.current
    seen.current = { session: sessionId, ids: new Set(current.map(turn => turn.id)) }
    if (!before || before.session !== sessionId || isStreaming || watched.current !== null) return
    const arrived = current.filter(turn => !before.ids.has(turn.id) && !turn.isStreaming)
    if (arrived.length !== 1) return // none, or a history page
    const [turn] = arrived
    if (turn.role === "assistant") sayRef.current(outcomeOf(current, latest.current.agent) || `${latest.current.agent} replied.`)
    else if (turn.role === "user" && turn.authorUserId) sayRef.current("New message.")
  }, [ids, sessionId, isStreaming])

  return <div role="status" aria-live="polite" className="sr-only"><span key={message.n}>{message.text}</span></div>
}
