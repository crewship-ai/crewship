"use client"

import { useEffect, useRef } from "react"

/**
 * Makes the hardware/gesture back button close an overlay instead of
 * navigating the page out from underneath it.
 *
 * Radix dialogs and sheets are not history entries, so on Android — and on iOS
 * where a back-swipe reaches Safari — back on an open overlay leaves the
 * surface behind it and the overlay's context with it. That is the sharpest
 * mobile-web defect left in this app, and it applies uniformly to every
 * dialog and sheet, so it is fixed once here rather than at ~79 call sites.
 *
 * Two decisions worth stating:
 *
 * It hooks mount, not an `open` prop. Radix content only exists while open,
 * and roughly half the call sites are uncontrolled (a `Trigger`, no `open`
 * state to read) — mount is the one signal both kinds share.
 *
 * Sheets only, deliberately. The same wiring on `DialogContent` pushed five
 * entries during a single conversation switch in the chat tests: dialog
 * content in this codebase mounts as part of ordinary rendering, so mount is
 * not a reliable stand-in for "somebody opened this", and the result would be
 * a back button that needs pressing five times. Sheets are also where the
 * gesture matters most — they are the drawer surface a phone actually uses.
 * Covering dialogs needs an `open`-driven signal, which is its own change.
 *
 * It closes by dispatching Escape rather than by calling a close function it
 * does not have. That routes through Radix's own dismissal path, so a dialog
 * that deliberately blocks Escape — an unsaved-changes guard, a running
 * operation — blocks back for the same reason, instead of the two disagreeing.
 */
export function useOverlayBackButton(enabled = true) {
  const pushed = useRef(false)

  useEffect(() => {
    if (!enabled || typeof window === "undefined") return

    // Spread the router's own state so Next's internal keys survive; a bare
    // object here makes the App Router lose its place.
    window.history.pushState({ ...window.history.state, __overlay: true }, "")
    pushed.current = true

    const onPop = () => {
      if (!pushed.current) return
      pushed.current = false
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      )
    }
    window.addEventListener("popstate", onPop)

    return () => {
      window.removeEventListener("popstate", onPop)
      pushed.current = false
      // Deliberately no `history.back()` here.
      //
      // Unwinding our own entry when the overlay closes by its own button is
      // tidier, and it is not worth what it costs. Several pages own their
      // history — chat writes the selected conversation with replaceState and
      // re-reads it on popstate (app/(dashboard)/chat/chat-client.tsx:73-136,
      // :383) — and a back() fired from an unmount races their writes. It
      // rewound a conversation switch in
      // components/features/conversations/__tests__/unified-chat.test.tsx.
      //
      // What is left behind is one history entry pointing at the URL the user
      // is already on, so the first back after closing an overlay by hand is a
      // no-op and the second leaves the page. That is a smaller defect than
      // undoing a navigation somebody else made.
    }
  }, [enabled])
}
