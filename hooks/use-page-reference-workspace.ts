"use client"

import { useEffect, useRef } from "react"
import type { PageLinkReference } from "@/lib/pages/stable-links"

// Adopt a pasted link once. A later explicit workspace-picker switch is new
// navigation intent, handled by the existing workspace dirty guard, not an
// instruction to bounce back to the link's workspace.
export function usePageReferenceWorkspace(
  reference: PageLinkReference | undefined,
  workspaceId: string,
  eligible: boolean,
  setWorkspaceId: (id: string | null) => void,
  clearReference: () => void,
) {
  const arrival = useRef<{ key: string; arrived: boolean } | null>(null)
  useEffect(() => {
    if (reference?.kind !== "page") { arrival.current = null; return }
    const key = JSON.stringify([reference.workspaceId, reference.pageId])
    if (arrival.current?.key !== key) arrival.current = { key, arrived: false }
    if (arrival.current.arrived && workspaceId !== reference.workspaceId) { clearReference(); return }
    if (!eligible) return
    if (workspaceId === reference.workspaceId) { arrival.current.arrived = true; return }
    setWorkspaceId(reference.workspaceId)
  }, [reference, workspaceId, eligible, setWorkspaceId, clearReference])
}
