"use client"

import { useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import { toast } from "sonner"

const WAIT_MS = 8000
const MARK_MS = 2500
const SETTLE_MS = 1000
const USER_INPUT = ["wheel", "touchstart", "pointerdown", "keydown"] as const

/**
 * Fired (with `{ sessionId }`) each time a search hit is scrolled into view, so
 * the transcript can release its follow-the-bottom lock (MessageAnchorRelease).
 */
export const MESSAGE_ANCHOR_EVENT = "crewship:message-anchor"

/**
 * Scrolls to the message named by `?message=<id>` (search results deep-link
 * here) once its turn is in the DOM.
 *
 * The target is captured when the param appears and handled by identity, not by
 * the live URL: the chat page rewrites the address (dropping `message`) while
 * it opens the session, which must not cancel a pending scroll, and picking a
 * hit inside the chat that is already open changes only `message`, which must
 * scroll again. History loads asynchronously, so it watches for the node for a
 * bounded time. A message that is not rendered as its own turn (merged into an
 * earlier grouped turn, outside the loaded window, deleted or no longer
 * visible) gets a neutral notice rather than a claim that it is gone.
 */
export function useMessageAnchor(sessionId: string | null) {
  const message = useSearchParams()?.get("message") ?? null
  const [seen, setSeen] = useState<string | null>(null)
  const [target, setTarget] = useState<{ id: string } | null>(null)
  // Derived-state update during render: a new `message` value becomes one pending target.
  if (message !== seen) {
    setSeen(message)
    if (message) setTarget({ id: message })
  }
  const handled = useRef<{ id: string } | null>(null)
  const settle = useRef<(() => void) | null>(null)
  useEffect(() => () => settle.current?.(), [])

  useEffect(() => {
    if (!sessionId || !target || handled.current === target) return
    const find = () => document.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(target.id)}"]`)
    const reveal = (el: HTMLElement) => {
      handled.current = target
      const place = () => {
        el.scrollIntoView({ block: "center" })
        // The transcript must not scroll back to its bottom (see MessageAnchorRelease).
        window.dispatchEvent(new CustomEvent(MESSAGE_ANCHOR_EVENT, { detail: { sessionId } }))
      }
      place()
      // Rows above the hit are still being measured (and Safari has no scroll anchoring), so keep the
      // hit centred for a short settle window, until the reader scrolls or interacts.
      settle.current?.()
      let frame = 0
      const until = Date.now() + SETTLE_MS
      const stop = () => { cancelAnimationFrame(frame); USER_INPUT.forEach((t) => window.removeEventListener(t, stop)); settle.current = null }
      const tick = () => {
        if (!el.isConnected || Date.now() >= until) { stop(); return }
        place()
        frame = requestAnimationFrame(tick)
      }
      USER_INPUT.forEach((t) => window.addEventListener(t, stop, { passive: true }))
      settle.current = stop
      frame = requestAnimationFrame(tick)
      el.setAttribute("data-search-target", "true")
      setTimeout(() => el.removeAttribute("data-search-target"), MARK_MS)
      setTarget(null)
    }
    const now = find()
    if (now) { reveal(now); return }
    let timer: ReturnType<typeof setTimeout> | undefined
    const observer = new MutationObserver(() => {
      const el = find()
      if (el) { observer.disconnect(); clearTimeout(timer); reveal(el) }
    })
    observer.observe(document.body, { childList: true, subtree: true })
    timer = setTimeout(() => {
      observer.disconnect()
      handled.current = target
      toast.message("Could not locate that message in this conversation view.")
      setTarget(null)
    }, WAIT_MS)
    return () => { observer.disconnect(); clearTimeout(timer) }
  }, [sessionId, target])
}
