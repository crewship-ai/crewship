"use client"

import { useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import { toast } from "sonner"

const WAIT_MS = 8000
const MARK_MS = 2500

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

  useEffect(() => {
    if (!sessionId || !target || handled.current === target) return
    const find = () => document.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(target.id)}"]`)
    const reveal = (el: HTMLElement) => {
      handled.current = target
      el.scrollIntoView({ block: "center" })
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
