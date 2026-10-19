"use client"

import { useEffect } from "react"

/**
 * The on-screen keyboard on a phone (phone pilot, MOBIL-DOPORUCENI 2a §5).
 * Mounted once by the app shell while the phone layout is on.
 *
 *  - `data-typing` on <html> while a text field has focus: globals.css hides
 *    the bottom tab bar then and zeroes --mobile-tab-bar-h, so the bar never
 *    sits between the field and the keyboard.
 *  - `--keyboard-inset`: how much of the layout viewport the keyboard covers
 *    (visualViewport), so the save bars and the toasts sit on top of it.
 */
export function PhoneKeyboard() {
  useEffect(() => {
    const root = document.documentElement
    const typing = (el: EventTarget | null) => {
      if (!(el instanceof HTMLElement)) return false
      if (el.isContentEditable) return true
      if (el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return true
      if (el instanceof HTMLInputElement) {
        return !["checkbox", "radio", "range", "color", "button", "submit", "reset", "file", "image", "hidden"].includes(el.type)
      }
      return false
    }
    const sync = () => {
      if (typing(document.activeElement)) root.dataset.typing = ""
      else delete root.dataset.typing
    }
    const onFocusIn = (e: FocusEvent) => {
      if (typing(e.target)) root.dataset.typing = ""
      else delete root.dataset.typing
    }
    // Focus may move nowhere (a tap on the page): check once it settled.
    const onFocusOut = () => { window.setTimeout(sync, 0) }
    document.addEventListener("focusin", onFocusIn)
    document.addEventListener("focusout", onFocusOut)
    sync()

    const vv = window.visualViewport
    const inset = () => {
      const covered = vv ? Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop)) : 0
      root.style.setProperty("--keyboard-inset", `${covered}px`)
    }
    inset()
    vv?.addEventListener("resize", inset)
    vv?.addEventListener("scroll", inset)
    return () => {
      document.removeEventListener("focusin", onFocusIn)
      document.removeEventListener("focusout", onFocusOut)
      vv?.removeEventListener("resize", inset)
      vv?.removeEventListener("scroll", inset)
      delete root.dataset.typing
      root.style.removeProperty("--keyboard-inset")
    }
  }, [])
  return null
}
