"use client"

import { useLayoutEffect, useRef } from "react"

/** Gap kept under the last content line when the spacer is active. */
const BOTTOM_GAP = 24

interface PinToTopSpacerProps {
  /** Incremented by the composer on every locally-sent message — arms the pin. */
  pinNonce: number
  /** data-turn-id of the turn to pin to the top (the just-sent user message). */
  pinTurnId: string | null
  sessionId: string
}

/**
 * ChatGPT's signature scroll move: after you send a message, your question
 * anchors at the TOP of the viewport and the reply streams in below it,
 * instead of everything hugging the bottom edge.
 *
 * Mechanism: this invisible spacer sits at the end of the conversation
 * content. On send it grows so that "scrolled to bottom" places the sent
 * message at the viewport top — StickToBottom then scrolls there on its own.
 * While the reply streams, the spacer shrinks 1:1 with content growth, so
 * total scroll height stays constant and the question stays pinned with zero
 * scroll movement. Once the reply outgrows the viewport the spacer reaches 0
 * and normal stick-to-bottom follow (with its escape + scroll pill) resumes.
 *
 * All sizing is done via direct style writes from a measuring layout effect —
 * no React state, so it never causes render loops.
 */
export function PinToTopSpacer({ pinNonce, pinTurnId, sessionId }: PinToTopSpacerProps) {
  const ref = useRef<HTMLDivElement>(null)
  const activeRef = useRef(false)
  const armedForRef = useRef(0)

  // Session swap: drop the spacer immediately.
  useLayoutEffect(() => {
    activeRef.current = false
    if (ref.current) ref.current.style.height = "0px"
  }, [sessionId])

  // Measure on every render while armed. The parent re-renders per streamed
  // frame, which is exactly the cadence the spacer needs to shrink at.
  useLayoutEffect(() => {
    if (pinNonce > 0 && pinNonce !== armedForRef.current) {
      armedForRef.current = pinNonce
      activeRef.current = true
    }
    if (!activeRef.current || !pinTurnId) return
    const spacer = ref.current
    const content = spacer?.parentElement
    const scroller = content?.parentElement
    if (!spacer || !content || !scroller) return
    const selector = typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(pinTurnId)
      : pinTurnId
    const target = content.querySelector<HTMLElement>(`[data-turn-id="${selector}"]`)
    if (!target) return
    const currentHeight = spacer.offsetHeight
    // Height from the pinned message's top to the end of real content (the
    // spacer itself excluded). Rect-based so nested offset parents don't lie.
    const tail = content.getBoundingClientRect().bottom - currentHeight - target.getBoundingClientRect().top
    const next = Math.max(0, Math.round(scroller.clientHeight - tail - BOTTOM_GAP))
    if (next !== currentHeight) spacer.style.height = `${next}px`
    // Reply outgrew the viewport — hand back to normal follow behavior.
    if (next === 0) activeRef.current = false
  })

  return <div ref={ref} aria-hidden="true" data-pin-spacer style={{ height: 0, flexShrink: 0 }} />
}
