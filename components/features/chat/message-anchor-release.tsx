"use client"

import { useEffect, useRef } from "react"
import { useStickToBottomContext } from "use-stick-to-bottom"
import { MESSAGE_ANCHOR_EVENT } from "@/hooks/use-message-anchor"

/**
 * Render inside <Conversation>. A transcript that follows its bottom keeps
 * scrolling there while history loads and rows are measured, and it ignores
 * scroll-ups during that time, so a jump to an earlier message was undone.
 * When useMessageAnchor scrolls to a hit in THIS session this releases the lock
 * (which also ends the running scroll animation): the view stays on the message
 * and a message that arrives later does not pull the reader away.
 *
 * Releasing must not strand the reader: when they send a message (`sendNonce`
 * changes) the lock is re-armed so the reply is followed.
 */
export function MessageAnchorRelease({ sessionId, sendNonce }: { sessionId: string; sendNonce: number }) {
  const { stopScroll, scrollToBottom } = useStickToBottomContext()
  useEffect(() => {
    const release = (event: Event) => {
      if ((event as CustomEvent<{ sessionId?: string }>).detail?.sessionId === sessionId) stopScroll()
    }
    window.addEventListener(MESSAGE_ANCHOR_EVENT, release)
    return () => window.removeEventListener(MESSAGE_ANCHOR_EVENT, release)
  }, [sessionId, stopScroll])
  const seen = useRef(sendNonce)
  useEffect(() => {
    if (sendNonce === seen.current) return
    seen.current = sendNonce
    void scrollToBottom()
  }, [sendNonce, scrollToBottom])
  return null
}
