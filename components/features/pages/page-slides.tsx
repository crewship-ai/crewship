"use client"

import * as React from "react"
import { SlidesWorkspace } from "@/components/features/slides/slides-workspace"
import { createHttpSlidesClient } from "@/lib/slides/http-client"

/**
 * A native Slides Page (behind NEXT_PUBLIC_SLIDES_EDITOR). The client is keyed to
 * the workspace so a workspace switch cannot carry a deck or a draft over.
 */
export function PageSlides({ workspaceId, pageId, name }: { workspaceId: string; pageId: string; name?: string }) {
  const client = React.useMemo(() => createHttpSlidesClient(workspaceId), [workspaceId])
  return <div className="flex-1 space-y-3 overflow-auto p-4">
    {name && <h1 className="text-lg font-semibold">{name}</h1>}
    <SlidesWorkspace key={`${workspaceId}:${pageId}`} pageId={pageId} client={client} />
  </div>
}
