"use client"

import { useCallback, useLayoutEffect } from "react"
import { registerHistoryNavigationGuard } from "@/lib/navigation-history-guard"
import { useNavigationGuard } from "@/hooks/use-navigation-guard"

/** Protect an editor's unsaved input across links, workspace changes and history. */
export function useUnsavedNavigationGuard(enabled: boolean, message: string) {
  const allow = useCallback(() => window.confirm(message), [message])
  useNavigationGuard(allow, enabled)

  useLayoutEffect(() => {
    if (!enabled) return
    let editorHref = window.location.href
    let editorState: unknown = window.history.state
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ""
    }
    const onPop = () => {
      const editor = new URL(editorHref)
      // A fragment or an overlay's same-address history entry leaves the
      // editor mounted. Let that navigation keep its normal behaviour.
      if (window.location.pathname + window.location.search === editor.pathname + editor.search) {
        editorHref = window.location.href
        editorState = window.history.state
        return true
      }
      if (allow()) return true
      // popstate arrives after history has moved. Restore the editor entry
      // before Next or selection listeners can unmount it. Only a refused
      // navigation writes history; accepting Back/Forward leaves it intact.
      window.history.pushState(editorState, "", editorHref)
      return false
    }
    window.addEventListener("beforeunload", beforeUnload)
    const unregister = registerHistoryNavigationGuard(onPop)
    return () => {
      window.removeEventListener("beforeunload", beforeUnload)
      unregister()
    }
  }, [enabled, allow])
}
