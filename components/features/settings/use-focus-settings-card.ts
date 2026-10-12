"use client"

import { useEffect } from "react"
import { settingsCardId } from "./shared"

/** How long to keep looking for the card while its pane loads its data. */
const WAIT_MS = 4000
const POLL_MS = 100
/** How long the card stays marked after the scroll. */
const MARK_MS = 2000

/**
 * Scrolls to the card a `?card=<slug>` deep link names, once the pane has
 * drawn it, and marks it (`data-focused`) for a moment so the eye lands on
 * it. ⌘K finds a setting by the card it lives in (#3045); landing at the top
 * of a long tab would make the person look for it a second time.
 */
export function useFocusSettingsCard(card: string, tab: string) {
  useEffect(() => {
    if (!card) return
    let marked: HTMLElement | null = null
    let unmark: ReturnType<typeof setTimeout> | undefined
    const started = Date.now()
    const poll = setInterval(() => {
      const el = document.getElementById(settingsCardId(card))
      if (!el && Date.now() - started < WAIT_MS) return
      clearInterval(poll)
      if (!el) return
      el.scrollIntoView({ block: "start", behavior: "smooth" })
      el.dataset.focused = "true"
      marked = el
      unmark = setTimeout(() => { delete el.dataset.focused }, MARK_MS)
    }, POLL_MS)
    return () => {
      clearInterval(poll)
      clearTimeout(unmark)
      if (marked) delete marked.dataset.focused
    }
  }, [card, tab])
}
