"use client"

import { useEffect } from "react"

export interface Shortcut {
  /** Single key (e.g. "j") or sequence (e.g. ["g","s"] for chord). */
  keys: string | [string, string]
  handler: (e: KeyboardEvent) => void
  /** When false the shortcut is ignored (e.g. modal open). Default true. */
  enabled?: boolean
}

function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  const tag = el.tagName
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true
  if (el.isContentEditable) return true
  return false
}

/**
 * Register keyboard shortcuts scoped to the document. Skips events originating
 * inside inputs/textareas/contenteditable to avoid stealing user typing.
 *
 * Chord shortcuts (tuple `keys`) require the first key within the last 1.5 s.
 */
export function useKeyboardShortcuts(shortcuts: Shortcut[]) {
  useEffect(() => {
    let pending: { key: string; expiresAt: number } | null = null

    const onKeyDown = (e: KeyboardEvent) => {
      if (isEditable(e.target)) return
      if (e.metaKey || e.ctrlKey || e.altKey) return

      for (const sc of shortcuts) {
        if (sc.enabled === false) continue
        if (typeof sc.keys === "string") {
          if (e.key === sc.keys) {
            sc.handler(e)
            return
          }
        } else {
          const [first, second] = sc.keys
          if (pending && pending.key === first && Date.now() < pending.expiresAt && e.key === second) {
            pending = null
            sc.handler(e)
            return
          }
          if (e.key === first) {
            pending = { key: first, expiresAt: Date.now() + 1500 }
            return
          }
        }
      }
    }

    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [shortcuts])
}
