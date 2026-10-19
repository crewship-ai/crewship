"use client"

import { useEffect } from "react"
import { toast } from "sonner"

const WAIT_MS = 8000

/**
 * Scrolls to the message named by `?message=<id>` (search results deep-link
 * here) once its turn is in the DOM. History loads asynchronously, so it
 * watches for the node for a bounded time and says so if the message is gone
 * (deleted, outside the loaded window, or no longer visible to the reader)
 * instead of leaving the reader at the bottom of the thread unexplained.
 */
export function useMessageAnchor(sessionId: string | null) {
  useEffect(() => {
    if (!sessionId) return
    const id = new URLSearchParams(window.location.search).get("message")
    if (!id) return
    const find = () => document.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(id)}"]`)
    let done = false
    const reveal = (el: HTMLElement) => {
      done = true
      el.scrollIntoView({ block: "center" })
      el.setAttribute("data-search-target", "true")
      el.classList.add("ring-2", "ring-primary/60", "rounded-md")
      setTimeout(() => el.classList.remove("ring-2", "ring-primary/60"), 2500)
    }
    const now = find()
    if (now) { reveal(now); return }
    const observer = new MutationObserver(() => { const el = find(); if (el) { observer.disconnect(); clearTimeout(timer); reveal(el) } })
    observer.observe(document.body, { childList: true, subtree: true })
    const timer = setTimeout(() => {
      observer.disconnect()
      if (!done) toast.message("That message is no longer available in this conversation.")
    }, WAIT_MS)
    return () => { observer.disconnect(); clearTimeout(timer) }
  }, [sessionId])
}
