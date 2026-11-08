"use client"

import * as React from "react"
import { Eye, ZoomIn, ZoomOut } from "lucide-react"
import { usePage } from "@/hooks/use-pages"
import { cn } from "@/lib/utils"
import { PageApplicationView } from "@/components/features/pages/page-application"
import { PageView } from "@/components/features/pages/page-view"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "./frame"
import { APP_DEVICES, APP_ZOOMS, CapabilityChips, deviceStyle, stepZoom, type AppDevice, type AppZoom } from "./kinds/app/app-view"
import type { AppCapability } from "./kinds/app/app-status"
import { FullScreenTool, useKindReport, type KindReport } from "./kinds/project-parts"

/** A published Pages application: its panels' routine actions, each confirmed by the viewer and checked by the server. */
export const PAGE_APPLICATION_CAPABILITIES: AppCapability[] = [
  { id: "actions", label: "Actions", detail: "Reads its panels and runs the routine actions they declare. Each action asks you first, and the server checks it." },
]

/**
 * A Page's content inside its Artifacts detail: the same application and
 * panel views Pages draws, read by the immutable page ID so a renamed or
 * reassigned slug never shows another page. A running application gets the
 * App slots (capabilities, full screen, device width and zoom).
 */
export function ArtifactPageContent({ workspaceId, pageId, onBack, onReport }: { workspaceId: string; pageId: string; onBack: () => void; onReport?: (report: KindReport | null) => void }) {
  const slug = "@" + pageId
  const detail = usePage(workspaceId, slug)
  const frame = useArtifactFrame()
  const phone = frame?.layout === "phone"
  const [running, setRunning] = React.useState(false)
  const [device, setDevice] = React.useState<AppDevice>("desktop")
  const [zoom, setZoom] = React.useState<AppZoom>("fit")
  const stage = React.useRef<HTMLDivElement>(null)
  useKindReport(onReport, { status: running ? { key: "live", label: "Running", detail: "The application runs isolated on its own origin." } : null, canEdit: false })
  const view = <PageView page={detail.page} slug={slug} loading={detail.loading} error={detail.error} notFound={detail.notFound}
    onBack={onBack} backLabel="All artifacts" workspaceId={workspaceId} live={detail.live} hosted />
  const step = (direction: 1 | -1) => setZoom(current => stepZoom(current, direction))
  const loaded = detail.page && !detail.loading && !detail.error && !detail.accessDenied
  return <div ref={stage} className={cn("relative flex min-h-[520px] flex-1 flex-col overflow-hidden", running && "bg-muted", running && (phone ? "pt-14" : "px-4 pb-16 pt-16"))}>
    <div className={cn("flex min-h-0 flex-1 flex-col", running && "items-center")}>
      <div data-app-frame={running ? "" : undefined} style={running ? deviceStyle(device, zoom, phone) : undefined}
        className={cn("flex min-h-0 w-full flex-1 flex-col overflow-hidden", running && !phone && "rounded-lg border bg-card shadow-sm")}>
        {loaded ? <PageApplicationView workspaceId={workspaceId} slug={slug} page={detail.raw} fallback={view} onAvailableChange={setRunning} /> : view}
      </div>
    </div>
    {/* Panels without an application draw their own header; the App slots belong to a running application only. */}
    {running && <>
      <ArtifactSlot name="view" label="View">
        <ArtifactTool icon={Eye} label="Preview" text={phone ? undefined : "Preview"} on />
        <ArtifactToolSeparator />
        <span role="group" aria-label="Capabilities" className="flex items-center gap-1 px-0.5"><CapabilityChips capabilities={PAGE_APPLICATION_CAPABILITIES} /></span>
      </ArtifactSlot>
      <ArtifactSlot name="output" label="Output"><FullScreenTool target={stage} /></ArtifactSlot>
      <ArtifactSlot name="viewport" label="Viewport">
        {APP_DEVICES.map(entry => <ArtifactTool key={entry.id} icon={entry.icon} label={entry.label} on={device === entry.id} onClick={() => setDevice(entry.id)} />)}
        <ArtifactToolSeparator />
        <ArtifactTool icon={ZoomOut} label="Zoom out" disabled={zoom === APP_ZOOMS[0]} onClick={() => step(-1)} />
        <button type="button" aria-label="Fit width" title="Fit width" aria-pressed={zoom === "fit"} onClick={() => setZoom("fit")}
          className="h-[30px] min-w-[3rem] rounded-[var(--radius-row)] px-1.5 font-mono text-xs text-muted-foreground hover:bg-accent hover:text-foreground coarse:min-h-[44px]">{zoom === "fit" ? "Fit" : `${zoom} %`}</button>
        <ArtifactTool icon={ZoomIn} label="Zoom in" disabled={zoom === APP_ZOOMS[APP_ZOOMS.length - 1]} onClick={() => step(1)} />
      </ArtifactSlot>
    </>}
  </div>
}
