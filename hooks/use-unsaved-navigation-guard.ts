"use client"

import { useCallback, useLayoutEffect, useRef } from "react"
import { registerHistoryNavigationGuard } from "@/lib/navigation-history-guard"
import { useNavigationGuard } from "@/hooks/use-navigation-guard"
import { askConfirm, confirmHostMounted } from "@/components/ui/confirm-host"

const HISTORY_INDEX_KEY = "__crewshipHistoryIndex"

/**
 * Protect an editor's unsaved input across links, workspace changes and history.
 *
 * Inside the application shell the question is the in-page ConfirmDialog
 * (Harbor 2, KIT-01): the navigation is refused at once and repeated when the
 * person chooses Leave. Without the shell (a standalone root, a unit test) it
 * falls back to the browser's confirm(). Reload and closing the tab keep the
 * browser's own prompt — a page cannot replace that one.
 */
export function useUnsavedNavigationGuard(enabled: boolean, message: string) {
  // A traversal repeated after Leave must not be asked about again.
  const passNextTraversal = useRef(false)

  const ask = useCallback(
    (retry: () => void): boolean => {
      if (!confirmHostMounted()) return window.confirm(message)
      void askConfirm({
        title: "Leave without saving?",
        description: message,
        confirmLabel: "Leave",
        cancelLabel: "Keep editing",
        destructive: true,
      }).then((ok) => {
        if (ok) retry()
      })
      return false
    },
    [message],
  )
  useNavigationGuard(ask, enabled)

  useLayoutEffect(() => {
    if (!enabled) return
    let editorHref = window.location.href
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ""
    }
    const onPop = (event: PopStateEvent) => {
      const editor = new URL(editorHref)
      // A fragment or an overlay's same-address history entry leaves the
      // editor mounted. Let that navigation keep its normal behaviour.
      if (window.location.pathname + window.location.search === editor.pathname + editor.search) {
        editorHref = window.location.href
        return true
      }
      if (passNextTraversal.current) {
        passNextTraversal.current = false
        return true
      }
      const target = (event.state as Record<string, unknown> | null)?.[HISTORY_INDEX_KEY]
      // The early dispatcher returns to this existing history entry and
      // suppresses the compensating popstate, preserving Forward history.
      // Leave then repeats the same traversal from wherever the editor is.
      return ask(() => {
        const now = (window.history.state as Record<string, unknown> | null)?.[HISTORY_INDEX_KEY]
        passNextTraversal.current = true
        if (Number.isInteger(target) && Number.isInteger(now)) window.history.go((target as number) - (now as number))
        else window.history.back()
      })
    }
    window.addEventListener("beforeunload", beforeUnload)
    const unregister = registerHistoryNavigationGuard(onPop)
    return () => {
      window.removeEventListener("beforeunload", beforeUnload)
      unregister()
    }
  }, [enabled, ask])
}
