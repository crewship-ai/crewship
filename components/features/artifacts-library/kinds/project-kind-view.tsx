"use client"

import * as React from "react"
import { ArtifactViewerProvider, type ArtifactViewerTarget } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { ArtifactViewerPanel, type ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { useArtifactFrame } from "../frame"
import { AppProjectView } from "./app/app-view"
import { DashboardProjectView } from "./dashboard/dashboard-view"
import type { KindReport } from "./project-parts"

/**
 * A project-backed App or Dashboard inside the ArtifactFrame. The project
 * viewer keeps every rule (permissions, drafts, comments, agent delivery);
 * this only lays its pieces out in the frame's slots and drawer. A project
 * that turns out to be Slides, a design system or an unsupported format
 * keeps the viewer's own layout.
 */
export function ProjectKindView({ workspaceId, target, kind, onBack, onReport }: {
  workspaceId: string
  target: ArtifactViewerTarget
  kind: "app" | "dashboard"
  onBack: () => void
  onReport?: (report: KindReport | null) => void
}) {
  const frame = useArtifactFrame()
  const openDrawer = frame?.openDrawer
  const layout = frame?.layout
  const onSection = React.useCallback((section: "history" | "comments" | null) => {
    // Back to the preview closes a drawer that covers it; a desktop drawer sits beside it and stays.
    if (section) openDrawer?.(section)
    else if (layout !== "desktop") openDrawer?.(null)
  }, [openDrawer, layout])
  const render = (parts: ArtifactViewerParts) => {
    if (parts.previewKind === "slides" || parts.previewKind === "design-system" || parts.previewKind === "unsupported") return undefined
    return kind === "dashboard"
      ? <DashboardProjectView workspaceId={workspaceId} parts={parts} onReport={onReport} />
      : <AppProjectView parts={parts} onReport={onReport} />
  }
  return <ArtifactViewerProvider workspaceId={workspaceId} target={target} onTargetChange={next => { if (!next) onBack() }}>
    <div className="flex min-h-[520px] flex-1 overflow-hidden"><ArtifactViewerPanel variant="library" render={render} onSection={onSection} /></div>
  </ArtifactViewerProvider>
}
