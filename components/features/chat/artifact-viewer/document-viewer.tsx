"use client"

import { useLayoutEffect, useRef } from "react"
import { ArrowLeft, Maximize2, Minimize2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { DocumentViewerProps, ViewerTarget } from "./types"
export type { DocumentViewerProps, ViewerTarget } from "./types"

function validTarget(value: ViewerTarget | null | undefined): value is ViewerTarget {
  return Boolean(value && value.type === "document" && value.mode === "latest" &&
    typeof value.workspaceId === "string" && value.workspaceId &&
    typeof value.pageId === "string" && value.pageId)
}
function sameTarget(a: ViewerTarget, b: ViewerTarget) {
  return a.workspaceId === b.workspaceId && a.pageId === b.pageId && a.type === b.type && a.mode === b.mode
}

/** Controlled presentation only. The host owns resolution, current authority and placement. */
export function DocumentViewer<Content>({ target, scopeKey, read, renderBody, layout, expanded, onClose, onExpandedChange, openerRef }: DocumentViewerProps<Content>) {
  const closeButton = useRef<HTMLButtonElement>(null)
  const identity = target ? JSON.stringify([scopeKey, target.workspaceId, target.pageId, target.type, target.mode]) : null
  const previousIdentity = useRef(identity)
  const restoreFrame = useRef<number | null>(null)
  const closeRequest = useRef<{identity: string | null; opener: HTMLElement | null; ref: DocumentViewerProps<Content>["openerRef"]} | null>(null)
  useLayoutEffect(() => {
    const previous = previousIdentity.current
    previousIdentity.current = identity
    if (identity) {
      closeRequest.current = null
      closeButton.current?.focus()
    } else if (previous && closeRequest.current?.identity === previous) {
      const request = closeRequest.current
      closeRequest.current = null
      // Restore only after the host actually closed, and cancel if it reopens.
      restoreFrame.current = requestAnimationFrame(() => {
        restoreFrame.current = null
        if (request.opener?.isConnected && request.ref?.current === request.opener) request.opener.focus()
      })
    }
    return () => {
      if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current)
      restoreFrame.current = null
    }
  }, [identity])

  if (!target) return null
  const authorized = scopeKey && validTarget(target) && read.status === "ready" &&
    read.scopeKey === scopeKey && validTarget(read.target) && sameTarget(target, read.target) &&
    typeof read.title === "string" && read.title.trim() ? read : undefined
  function close() {
    closeRequest.current = {identity, opener: openerRef?.current ?? null, ref: openerRef}
    onClose()
  }
  const mobile = layout === "mobile"
  const CloseIcon = mobile ? ArrowLeft : X
  return <section
    aria-label="Document preview"
    aria-busy={read.status === "loading"}
    data-layout={layout}
    data-expanded={expanded}
    className="flex h-full min-w-0 flex-col overflow-hidden border-l bg-background"
    onKeyDown={event => {
      if (event.key !== "Escape" || event.defaultPrevented) return
      event.preventDefault()
      event.stopPropagation()
      close()
    }}
  >
    <header className="flex shrink-0 items-center gap-2 border-b p-3">
      <Button ref={closeButton} type="button" variant="ghost" size={mobile ? "default" : "icon"}
        className="coarse:min-h-12 coarse:min-w-12" aria-label={mobile ? "Back to chat" : "Close document preview"} onClick={close}>
        <CloseIcon className="size-4" aria-hidden />{mobile && <span>Back to chat</span>}
      </Button>
      <div className="min-w-0 flex-1">
        <h2 className="truncate font-semibold">{authorized ? authorized.title : "Document preview"}</h2>
        <p className="text-xs text-muted-foreground">Current saved document</p>
      </div>
      {!mobile && <Button type="button" variant="ghost" size="icon" className="coarse:min-h-12 coarse:min-w-12"
        aria-label={expanded ? "Show chat alongside" : "Expand document preview"} aria-pressed={expanded}
        onClick={() => onExpandedChange(!expanded)}>
        {expanded ? <Minimize2 className="size-4" aria-hidden /> : <Maximize2 className="size-4" aria-hidden />}
      </Button>}
    </header>
    <div className="min-h-0 min-w-0 flex-1 overflow-auto p-4">
      {authorized ? <div aria-label="Document content">{renderBody(authorized)}</div>
        : <p role={read.status === "denied" || read.status === "error" ? "alert" : "status"}>
          {read.status === "loading" ? "Loading document…"
            : read.status === "denied" ? "Document access is unavailable."
            : read.status === "error" ? "Document could not be loaded. Try reopening the preview."
            : "Preview unavailable. Reopen the saved document to check access."}
        </p>}
    </div>
  </section>
}
