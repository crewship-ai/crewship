"use client"

import { useEffect } from "react"
import { useStickToBottomContext } from "use-stick-to-bottom"

/** Fired by useMessageAnchor right after it scrolls a search hit into view. */
export const MESSAGE_ANCHOR_EVENT = "crewship:message-anchor"

/**
 * Render inside <Conversation>. A transcript that follows its bottom keeps
 * scrolling there while history loads and rows are measured, and it ignores
 * scroll-ups during that time, so a jump to an earlier message was undone.
 * On the anchor event this releases the lock (which also ends the running
 * scroll animation): the view stays on the message, a message that arrives
 * later does not pull the reader away, and scrolling back to the bottom
 * resumes following.
 */
export function MessageAnchorRelease() {
  const { stopScroll } = useStickToBottomContext()
  useEffect(() => {
    const release = () => stopScroll()
    window.addEventListener(MESSAGE_ANCHOR_EVENT, release)
    return () => window.removeEventListener(MESSAGE_ANCHOR_EVENT, release)
  }, [stopScroll])
  return null
}
