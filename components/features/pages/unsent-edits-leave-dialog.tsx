"use client"

import * as React from "react"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import type { EditorNavigation } from "@/components/features/pages/editor/use-editor-route"

/**
 * The question a native Document or Slides asks when an in-app link would leave
 * edits the server has not confirmed. Without it the route guard refuses the
 * link and nothing is said.
 *
 * It promises nothing about a local copy: whether this browser kept one depends
 * on its storage, and the editor's save status says so itself.
 */
export function UnsentEditsLeaveDialog({ pending, onReturnFocus }: {
  pending: EditorNavigation["pending"]
  /**
   * Where focus goes when the answer keeps you here. The dialog is
   * open-controlled with no trigger, so Radix would otherwise drop focus on
   * `<body>` after Keep editing or Escape.
   */
  onReturnFocus: () => void
}) {
  // Radix closes the dialog after an action runs and reports the close with the
  // same `pending`; answer once so Leave does not navigate twice.
  const answered = React.useRef(false)
  React.useEffect(() => {
    if (pending != null) answered.current = false
  }, [pending])
  const answer = (choice: "keep" | "leave") => {
    if (answered.current) return
    answered.current = true
    if (choice === "keep") {
      pending?.keep()
      window.requestAnimationFrame(onReturnFocus)
    } else pending?.discard()
  }
  return (
    <AlertDialog open={pending != null} onOpenChange={(open) => { if (!open) answer("keep") }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Leave with unsent edits?</AlertDialogTitle>
          <AlertDialogDescription>
            Some of your edits have not been accepted by the server. If you leave now they may be lost; the save status
            in the editor says whether this browser kept a copy.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel className="coarse:min-h-[44px]" onClick={() => answer("keep")}>Keep editing</AlertDialogCancel>
          <AlertDialogAction className="coarse:min-h-[44px]" onClick={() => answer("leave")}>Leave anyway</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
