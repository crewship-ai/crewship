"use client"

import { useEffect } from "react"

/**
 * Makes the hardware/gesture back button close an overlay instead of
 * navigating the page out from underneath it.
 *
 * Radix sheets are not history entries, so on Android — and on iOS where a
 * back-swipe reaches Safari — back on an open drawer leaves the surface behind
 * it and the drawer's context with it. That is the sharpest mobile-web defect
 * left in this app, and it applies uniformly to every sheet, so it is fixed
 * once here rather than at each call site.
 *
 * Three decisions worth stating:
 *
 * It hooks mount, not an `open` prop. Radix content only exists while open,
 * and roughly half the call sites are uncontrolled (a `Trigger`, no `open`
 * state to read) — mount is the one signal both kinds share.
 *
 * Sheets only, deliberately. The same wiring on `DialogContent` pushed five
 * entries during a single conversation switch in the chat tests: dialog
 * content in this codebase mounts as part of ordinary rendering, so mount is
 * not a reliable stand-in for "somebody opened this". Sheets are also where the
 * gesture matters most — they are the drawer surface a phone actually uses.
 * Covering dialogs needs an `open`-driven signal, which is its own change.
 *
 * It closes by dispatching Escape rather than by calling a close function it
 * does not have. That routes through Radix's own dismissal path, so a sheet
 * that deliberately blocks Escape — an unsaved-changes guard, a running
 * operation — blocks back for the same reason, instead of the two disagreeing.
 */

/**
 * Open overlays, innermost last.
 *
 * One `popstate` reaches every listener on `window`, so an earlier version —
 * where each overlay answered for itself — dismissed the whole stack at once:
 * with a sheet opened from inside a sheet, one back dispatched two Escapes,
 * Radix closed only the top layer, and the outer one was left open having
 * already spent its history entry. The next back then navigated away with it
 * still showing, which is the exact failure this hook exists to prevent. A
 * shared stack means one back press is answered by exactly one overlay, the
 * innermost — which is what a person pressing it means.
 */
const stack: symbol[] = []

const isTop = (token: symbol) => stack.length > 0 && stack[stack.length - 1] === token

const holdsOwnEntry = () =>
  (window.history.state as { __overlay?: boolean } | null)?.__overlay === true

export function useOverlayBackButton(enabled = true) {
  useEffect(() => {
    if (!enabled || typeof window === "undefined") return

    const token = Symbol("overlay")
    stack.push(token)
    // Spread the router's own state so Next's internal keys survive; a bare
    // object here makes the App Router lose its place.
    window.history.pushState({ ...window.history.state, __overlay: true }, "")

    const onPop = () => {
      if (!isTop(token)) return
      stack.pop()
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      )
    }
    window.addEventListener("popstate", onPop)

    return () => {
      window.removeEventListener("popstate", onPop)
      const wasTop = isTop(token)
      const i = stack.lastIndexOf(token)
      if (i !== -1) stack.splice(i, 1)

      // Unwind our own entry when the overlay closed by its own button, so
      // opening and closing a drawer five times does not cost five back
      // presses to leave the page afterwards.
      //
      // Two guards, both learned by breaking something. Only the innermost
      // overlay may unwind, or a stack collapses out of order. And only while
      // our marker is still the current entry: if something inside the overlay
      // navigated, the router has pushed over it and going back here would
      // undo that navigation instead — chat writes its selected conversation
      // with replaceState and re-reads it on popstate
      // (app/(dashboard)/chat/chat-client.tsx:73-136, :383), and is the
      // surface that showed this.
      if (wasTop && holdsOwnEntry()) window.history.back()
    }
  }, [enabled])
}
