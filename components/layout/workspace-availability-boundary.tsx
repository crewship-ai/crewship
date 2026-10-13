"use client"

import { useLayoutEffect } from "react"
import { Dialog } from "radix-ui"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"

/** Keep the current route/draft mounted; unavailable is not proof of revocation. */
export function WorkspaceAvailabilityBoundary({children}: {children: React.ReactNode}) {
  const {workspaceId, workspaceUnavailable, workspaces, refresh, setWorkspaceId} = useWorkspace()
  useLayoutEffect(() => {
    if (!workspaceUnavailable) return
    // Portals escape the route's DOM wrapper. Preserve each dialog's own
    // attributes, isolate it for this boundary only, then restore exactly.
    const previous = new Map<HTMLElement, {aria: string | null; inert: string | null}>()
    const isolate = () => {
      for (const dialog of document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')) {
        if (dialog.closest("[data-workspace-recovery]")) continue
        if (!previous.has(dialog)) previous.set(dialog, {aria: dialog.getAttribute("aria-hidden"), inert: dialog.getAttribute("inert")})
        dialog.setAttribute("aria-hidden", "true")
        dialog.setAttribute("inert", "")
      }
    }
    isolate()
    const observer = new MutationObserver(isolate)
    observer.observe(document.body, {childList: true, subtree: true})
    return () => {
      observer.disconnect()
      for (const [dialog, attributes] of previous) {
        for (const [name, value] of [["aria-hidden", attributes.aria], ["inert", attributes.inert]] as const) {
          if (value === null) dialog.removeAttribute(name)
          else dialog.setAttribute(name, value)
        }
      }
    }
  }, [workspaceUnavailable])
  const block = (event: React.SyntheticEvent) => {
    if (workspaceUnavailable) {event.preventDefault();event.stopPropagation()}
  }
  return <>
    <div key={workspaceId ?? "no-workspace"} className={workspaceUnavailable ? "hidden" : "contents"}
      inert={workspaceUnavailable || undefined} aria-hidden={workspaceUnavailable || undefined}
      onClickCapture={block} onKeyDownCapture={block} onSubmitCapture={block}>
      {children}
    </div>
    <Dialog.Root open={workspaceUnavailable}>
      <Dialog.Portal>
        {/* Opaque coverage includes existing portaled editors; Radix hides other
            content from assistive technology and owns focus until recovery. */}
        <Dialog.Overlay className="fixed inset-0 z-50 bg-background" />
        <Dialog.Content data-workspace-recovery="" className="fixed left-1/2 top-1/2 z-50 grid max-h-[85dvh] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 overflow-auto rounded-lg border bg-background p-6"
          onEscapeKeyDown={e=>e.preventDefault()} onInteractOutside={e=>e.preventDefault()}>
          <Dialog.Title className="text-base font-semibold">Selected workspace is unavailable</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            It may be temporarily unavailable or your membership may have changed. Your selection and local draft are preserved while you retry. Choosing another workspace asks before discarding unsaved changes.
          </Dialog.Description>
          <Button onClick={()=>{void refresh()}}>Retry workspace</Button>
          {workspaces.map(workspace=><Button key={workspace.id} variant="outline" onClick={()=>setWorkspaceId(workspace.id)}>Choose {workspace.name}</Button>)}
          {workspaces.length === 0 && <Button variant="outline" onClick={()=>setWorkspaceId(null)}>Continue without a workspace</Button>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </>
}
