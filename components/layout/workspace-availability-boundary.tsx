"use client"

import { useLayoutEffect, useRef } from "react"
import { Building2 } from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"

function roleLabel(role: string | null | undefined): string | undefined {
  if (!role) return undefined
  return role.charAt(0).toUpperCase() + role.slice(1).toLowerCase()
}

/**
 * Keep the current route/draft mounted; unavailable is not proof of revocation.
 *
 * Harbor 2 G04 (v67 "workspace unavailable"): a blocking CreateSurface — no
 * ×, no Esc, no overlay click, no drag — over the opaque page ground, so no
 * data of a workspace the person may no longer belong to shows through. The
 * page under it stays mounted, hidden and inert, so a draft survives a Retry.
 * Switching to another workspace still goes through the unsaved-changes guard.
 */
export function WorkspaceAvailabilityBoundary({children, enabled = true}: {children: React.ReactNode; enabled?: boolean}) {
  const {workspaceId, workspaceUnavailable: unavailable, workspaces, refresh, setWorkspaceId} = useWorkspace()
  const workspaceUnavailable = enabled && unavailable
  // The name of the workspace that went away: once it is gone from the list
  // only the last list that had it knows what it was called.
  const names = useRef(new Map<string, string>())
  for (const w of workspaces) names.current.set(w.id, w.name)
  const goneName = workspaceId ? names.current.get(workspaceId) : undefined
  useLayoutEffect(() => {
    if (!workspaceUnavailable) return
    // Portals escape the route's DOM wrapper. Preserve each dialog's own
    // attributes, isolate it for this boundary only, then restore exactly.
    const previous = new Map<HTMLElement, {aria: string | null; inert: string | null}>()
    const isolate = () => {
      for (const dialog of document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')) {
        if (dialog.closest("[data-workspace-recovery]") || dialog.querySelector("[data-workspace-recovery]")) continue
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
  const title = goneName ? `${goneName} is unavailable` : "This workspace is unavailable"
  return <>
    <div key={enabled ? workspaceId ?? "no-workspace" : "account"} className={workspaceUnavailable ? "hidden" : "contents"}
      inert={workspaceUnavailable || undefined} aria-hidden={workspaceUnavailable || undefined}
      onClickCapture={block} onKeyDownCapture={block} onSubmitCapture={block}>
      {children}
    </div>
    <CreateSurface open={workspaceUnavailable} onOpenChange={() => {}} blocking phoneHeight="full" size="sm" ariaLabel={title}>
      <div data-workspace-recovery="" className="contents">
        <CreateSurfaceHeader
          icon={Building2}
          accent="amber"
          title={title}
          description="Your membership may have changed. Your drafts are kept while you retry."
        />
        {workspaces.length > 0 && (
          <CreateSurfaceBody>
            <p className="mb-2 text-xs font-medium text-muted-foreground">Or switch to</p>
            <div className="flex flex-col gap-2">
              {workspaces.map((workspace) => (
                <CreateSurfaceTile
                  key={workspace.id}
                  icon={Building2}
                  title={workspace.name}
                  meta={roleLabel(workspace.currentUserRole)}
                  onClick={() => setWorkspaceId(workspace.id)}
                />
              ))}
            </div>
          </CreateSurfaceBody>
        )}
        <CreateSurfaceFooter
          hint={null}
          // Product rule: leaving without a workspace is offered only when
          // there is no other workspace to switch to.
          onCancel={workspaces.length === 0 ? () => setWorkspaceId(null) : undefined}
          cancelLabel="Continue without a workspace"
          guardCancel={false}
          primaryLabel="Retry"
          onPrimary={() => { void refresh() }}
        />
      </div>
    </CreateSurface>
  </>
}
