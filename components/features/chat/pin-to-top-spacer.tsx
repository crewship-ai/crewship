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

/** Layout-box top of `el` relative to `ancestor`, walking the offsetParent
 *  chain. Unlike getBoundingClientRect this ignores CSS transforms, so the
 *  arrival animation's translateY doesn't corrupt the measurement. Returns
 *  null when `ancestor` is not on the chain (unexpected DOM shape). */
function layoutTopWithin(el: HTMLElement, ancestor: HTMLElement): number | null {
  let top = 0
  let node: HTMLElement | null = el
  while (node && node !== ancestor) {
    top += node.offsetTop
    node = node.offsetParent as HTMLElement | null
  }
  return node === ancestor ? top : null
}

/**
 * Pin-to-top scroll pattern: after you send a message, your question anchors
 * at the TOP of the viewport and the reply streams in below it, instead of
 * everything hugging the bottom edge.
 *
 * Mechanism: this invisible spacer sits at the end of the conversation
 * content. On send it grows so that "scrolled to bottom" places the sent
 * message at the viewport top — StickToBottom then scrolls there on its own.
 * While the reply streams, the spacer shrinks 1:1 with content growth, so
 * total scroll height stays constant and the question stays pinned with zero
 * scroll movement. Once the reply outgrows the viewport the spacer reaches 0
 * and normal stick-to-bottom follow (with its escape + scroll pill) resumes.
 *
 * Sizing happens via direct style writes from a measuring routine driven by
 * (a) parent renders and (b) a ResizeObserver on the content element — the
 * smooth-text reveal grows content from leaf-local state between parent
 * renders, so render-driven measurement alone would let the pin drift.
 * The element is display:none while inactive so it never contributes a flex
 * gap to the conversation column.
 */
export function PinToTopSpacer({ pinNonce, pinTurnId, sessionId }: PinToTopSpacerProps) {
  const ref = useRef<HTMLDivElement>(null)
  const activeRef = useRef(false)
  // Initialized to the CURRENT nonce so a remount mid-conversation doesn't
  // spuriously re-arm a pin from a long-finished send.
  const armedForRef = useRef(pinNonce)
  const pinTurnIdRef = useRef(pinTurnId)
  pinTurnIdRef.current = pinTurnId
  const observerRef = useRef<ResizeObserver | null>(null)

  const deactivate = () => {
    activeRef.current = false
    observerRef.current?.disconnect()
    observerRef.current = null
    const spacer = ref.current
    if (spacer) {
      spacer.style.height = "0px"
      spacer.style.display = "none"
    }
  }

  const measure = () => {
    if (!activeRef.current) return
    const turnId = pinTurnIdRef.current
    const spacer = ref.current
    const content = spacer?.parentElement
    const scroller = content?.parentElement
    if (!turnId || !spacer || !content || !scroller) return
    const selector = typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(turnId)
      : turnId
    const target = content.querySelector<HTMLElement>(`[data-turn-id="${selector}"]`)
    if (!target) return
    const targetTop = layoutTopWithin(target, scroller)
    const contentTop = layoutTopWithin(content, scroller)
    if (targetTop === null || contentTop === null) return
    const currentHeight = spacer.offsetHeight
    // Height from the pinned message's top to the end of real content (the
    // spacer itself excluded), in transform-free layout coordinates.
    const tail = contentTop + content.offsetHeight - currentHeight - targetTop
    const next = Math.max(0, Math.round(scroller.clientHeight - tail - BOTTOM_GAP))
    if (next === 0) {
      // Reply outgrew the viewport — hand back to normal follow behavior.
      deactivate()
      return
    }
    spacer.style.display = "block"
    if (next !== currentHeight) spacer.style.height = `${next}px`
  }
  const measureRef = useRef(measure)
  measureRef.current = measure

  // Session swap: drop the spacer immediately.
  useLayoutEffect(() => {
    deactivate()
  }, [sessionId])

  // Disconnect the observer on unmount.
  useLayoutEffect(() => () => { observerRef.current?.disconnect() }, [])

  // Arm on a new send; measure on every parent render while active. The
  // ResizeObserver covers growth that happens BETWEEN parent renders (the
  // smooth-text reveal advances via leaf-local state).
  useLayoutEffect(() => {
    if (pinNonce > 0 && pinNonce !== armedForRef.current) {
      armedForRef.current = pinNonce
      activeRef.current = true
      // Any child growth changes the content element's own height, so
      // observing content alone covers turns added or grown mid-stream.
      // Our compensating shrink inside the callback triggers one extra
      // (no-op) observation round and then settles.
      const content = ref.current?.parentElement
      if (content && typeof ResizeObserver !== "undefined" && !observerRef.current) {
        const ro = new ResizeObserver(() => measureRef.current())
        ro.observe(content)
        observerRef.current = ro
      }
    }
    measureRef.current()
  })

  return <div ref={ref} aria-hidden="true" data-pin-spacer style={{ height: 0, display: "none", flexShrink: 0 }} />
}
