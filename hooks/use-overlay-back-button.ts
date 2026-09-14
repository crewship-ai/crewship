"use client"

import { useEffect, useRef } from "react"

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

/**
 * Set while an overlay unwinds its own entry with `history.back()`. That call
 * fires a `popstate` like any other, and with a sheet inside a sheet the outer
 * one answered it: closing the inner drawer by its × closed the outer one too.
 * The next `popstate` after an unwind is ours, not the person's, and is
 * swallowed once — by whichever overlay is left on the stack to be fooled.
 */
let unwinding = false

const isTop = (token: symbol) => stack.length > 0 && stack[stack.length - 1] === token

const holdsOwnEntry = () =>
  (window.history.state as { __overlay?: boolean } | null)?.__overlay === true

const pushOwnEntry = () =>
  // Spread the router's own state so Next's internal keys survive; a bare
  // object here makes the App Router lose its place.
  window.history.pushState({ ...window.history.state, __overlay: true }, "")

/**
 * @param stillOpen Answers "did the overlay refuse to close?" after Escape
 *   was dispatched for a back press. Radix prevents the Escape event's default
 *   whether it dismisses or is told not to, so the event itself cannot say;
 *   what can is the content's own `data-state` once React has flushed. Given
 *   this, an overlay that blocks Escape keeps its history entry — the next
 *   back asks it again rather than navigating underneath it. Without it, the
 *   entry is treated as spent on the first back.
 */
export function useOverlayBackButton(enabled = true, stillOpen?: () => boolean) {
  const stillOpenRef = useRef(stillOpen)
  stillOpenRef.current = stillOpen

  useEffect(() => {
    if (!enabled || typeof window === "undefined") return

    const token = Symbol("overlay")
    stack.push(token)
    pushOwnEntry()

    let settle: number | undefined

    const onPop = () => {
      if (unwinding) {
        unwinding = false
        return
      }
      if (!isTop(token)) return
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      )
      const probe = stillOpenRef.current
      if (!probe) {
        stack.pop()
        return
      }
      // Ownership is decided once Radix has answered, not before. The state
      // update from its dismissal is flushed in a microtask, so by the next
      // task the content either carries `data-state="closed"` (dismissing,
      // entry spent — a second back during the exit animation should reach
      // the page) or is still open (refused — put the entry back so back
      // keeps asking this overlay).
      settle = window.setTimeout(() => {
        settle = undefined
        const i = stack.lastIndexOf(token)
        if (i === -1) return
        if (probe()) {
          pushOwnEntry()
        } else {
          stack.splice(i, 1)
        }
      }, 0)
    }
    window.addEventListener("popstate", onPop)

    return () => {
      window.removeEventListener("popstate", onPop)
      if (settle !== undefined) window.clearTimeout(settle)
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
      if (wasTop && holdsOwnEntry()) {
        // Only an overlay still on the stack can mistake the unwind for a
        // back press; with nobody left to fool, a flag set here would sit
        // until the next sheet opened and swallow its first real back.
        unwinding = stack.length > 0
        window.history.back()
      } else if (stack.length === 0) {
        unwinding = false
      }
    }
  }, [enabled])
}
