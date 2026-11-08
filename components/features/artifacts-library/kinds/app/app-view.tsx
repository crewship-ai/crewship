"use client"

import * as React from "react"
import Link from "next/link"
import { Code2, Eye, FileCode2, MessageSquarePlus, Monitor, RotateCw, ShieldAlert, ShieldCheck, Smartphone, Tablet, User, Database, ZoomIn, ZoomOut, type LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "../../frame"
import { FullScreenTool, ProjectDrawer, ScreenMenu, VersionMenu, useKindReport, type KindReport } from "../project-parts"
import { appCapabilities, appFrameStatus, runtimeMissing, type AppCapability } from "./app-status"

export type AppDevice = "desktop" | "tablet" | "phone"
export const APP_DEVICES: { id: AppDevice; label: string; icon: LucideIcon; width: number | null }[] = [
  { id: "desktop", label: "Desktop · full width", icon: Monitor, width: null },
  { id: "tablet", label: "Tablet · 820", icon: Tablet, width: 820 },
  { id: "phone", label: "Phone · 390", icon: Smartphone, width: 390 },
]
export const APP_ZOOMS = [50, 75, 100, 125] as const
export type AppZoom = "fit" | typeof APP_ZOOMS[number]

const CAP_ICON: Record<AppCapability["id"], LucideIcon> = { user: User, data: Database, actions: ShieldCheck }

/** The chips under the view slot: the declared capabilities, or "Static · no capabilities". */
export function CapabilityChips({ capabilities }: { capabilities: AppCapability[] }) {
  const chip = "inline-flex h-6 items-center gap-1 rounded-full border bg-background px-2 text-xs text-muted-foreground"
  if (!capabilities.length) return <span className={chip} title="It shows its own files and nothing else: no user, data, model or connections."><FileCode2 aria-hidden className="size-3" />Static · no capabilities</span>
  return <>
    <span className="inline-flex items-center gap-1 px-1 text-xs text-muted-foreground"><ShieldCheck aria-hidden className="size-3" />Uses</span>
    {capabilities.map(capability => {
      const Icon = CAP_ICON[capability.id]
      return <span key={capability.id} className={chip} title={capability.detail} aria-label={`${capability.label}: ${capability.detail}`}><Icon aria-hidden className="size-3" />{capability.label}</span>
    })}
  </>
}

/** Device width and zoom of the preview frame; the phone layout always fills the width. */
export function deviceStyle(device: AppDevice, zoom: AppZoom, phoneLayout: boolean): React.CSSProperties {
  if (phoneLayout) return {}
  const width = APP_DEVICES.find(entry => entry.id === device)?.width
  return { ...(width ? { width, maxWidth: "100%" } : {}), ...(zoom === "fit" ? {} : { zoom: zoom / 100 }) }
}

export const stepZoom = (zoom: AppZoom, step: 1 | -1): AppZoom => {
  const index = zoom === "fit" ? APP_ZOOMS.indexOf(100) : APP_ZOOMS.indexOf(zoom)
  return APP_ZOOMS[Math.max(0, Math.min(APP_ZOOMS.length - 1, index + step))]
}

/** An interactive app on a server without an isolating runtime origin: said plainly in the frame, never a blank stage. */
export function RuntimeGate() {
  return <div role="alert" data-gate="runtime" className="grid h-full content-center justify-items-center gap-2 p-6 text-center text-sm">
    <span aria-hidden className="flex size-10 items-center justify-center rounded-full border bg-muted text-muted-foreground"><ShieldAlert className="size-5" /></span>
    <b className="font-semibold">This app can&apos;t run here yet</b>
    <p className="max-w-prose text-muted-foreground">Its own code runs only on a separate application domain, and this installation has none configured. An operator sets CREWSHIP_PAGE_RUNTIME_ORIGIN. Nothing was loaded.</p>
  </div>
}

/**
 * One App, laid out like every other (Harbor 2 v87): view (Preview, version,
 * page) with the capability chips under it, output (Reload, full screen,
 * edit source), viewport (device width, zoom), Comment in tools, and the
 * comment queue, history and Propose in the drawer. Only controls with a
 * backend path appear; there is no Source or Compare view and no export yet.
 */
export function AppProjectView({ parts, onReport }: { parts: ArtifactViewerParts; onReport?: (report: KindReport | null) => void }) {
  const frame = useArtifactFrame()
  const phone = frame?.layout === "phone"
  const [device, setDevice] = React.useState<AppDevice>("desktop")
  const [zoom, setZoom] = React.useState<AppZoom>("fit")
  const [reloads, setReloads] = React.useState(0)
  const stage = React.useRef<HTMLDivElement>(null)
  const { data, version } = parts
  const capabilities = appCapabilities(data.interactive, parts.canWrite && !version.viewingOld)
  const gate = parts.previewKind === "interactive" && runtimeMissing(data.interactive)
  useKindReport(onReport, {
    status: appFrameStatus({ interactive: data.interactive, canWrite: parts.canWrite, canComment: parts.canComment, viewingOld: version.viewingOld, shown: version.shown }),
    canEdit: parts.canWrite && parts.maySend,
  })

  return <div ref={stage} data-app-stage data-device={phone ? "phone" : device}
    className={cn("@container flex min-h-0 flex-1 flex-col bg-muted", phone ? "pb-28 pt-[5.75rem]" : "px-4 pb-16 pt-[5.5rem]")}>
    <div className={cn("grid w-full gap-2 empty:hidden", phone ? "px-2" : "mx-auto max-w-5xl")}>{parts.banners}{parts.select.hint}</div>
    <div className={cn("flex min-h-0 flex-1 justify-center", !phone && "mt-2")}>
      <div data-app-frame style={deviceStyle(device, zoom, phone)}
        className={cn("relative flex min-h-[420px] w-full min-w-0 flex-col overflow-hidden bg-card", !phone && "rounded-lg border shadow-sm")}>
        <div className="flex h-7 shrink-0 items-center gap-2 border-b bg-muted/60 px-2.5 text-xs text-muted-foreground">
          <span className="min-w-0 flex-1 truncate font-mono">{parts.labelOf(parts.screen) || data.name || "app"} · v{version.shown}</span>
          <span className="inline-flex shrink-0 items-center gap-1" title="Own origin. No access to your Crewship session, cookies or credentials."><ShieldCheck aria-hidden className="size-3" />Runs isolated</span>
        </div>
        <div key={reloads} ref={parts.canvasRef} className="relative min-h-0 flex-1 bg-white">{gate ? <RuntimeGate /> : parts.preview}</div>
        {/* The composer floats over the preview on a wide stage and follows it on a narrow one. */}
        {parts.pinCard}
      </div>
    </div>

    <ArtifactSlot name="view" label="View" className="flex-col items-start gap-1">
      <div className="flex items-center gap-0.5">
        <ArtifactTool icon={Eye} label="Preview" text={phone ? undefined : "Preview"} on />
        <ArtifactToolSeparator />
        <VersionMenu parts={parts} />
        <ScreenMenu parts={parts} />
        {parts.share}
      </div>
      <div role="group" aria-label="Capabilities" className="flex items-center gap-1 px-0.5 pb-0.5">
        <CapabilityChips capabilities={capabilities} />
      </div>
    </ArtifactSlot>
    <ArtifactSlot name="output" label="Output">
      <ArtifactTool icon={RotateCw} label="Reload app" text={phone ? undefined : "Reload"} onClick={() => setReloads(count => count + 1)} />
      <FullScreenTool target={stage} />
      {parts.editSourceHref && <Link href={parts.editSourceHref} aria-label="Edit source" title="Edit source"
        className="inline-flex h-[30px] min-w-[30px] items-center justify-center rounded-[var(--radius-row)] px-[7px] text-muted-foreground hover:bg-accent hover:text-foreground coarse:min-h-[44px] coarse:min-w-[44px]"><Code2 aria-hidden className="size-4" /></Link>}
    </ArtifactSlot>
    <ArtifactSlot name="viewport" label="Viewport">
      {APP_DEVICES.map(entry => <ArtifactTool key={entry.id} icon={entry.icon} label={entry.label} on={device === entry.id} onClick={() => setDevice(entry.id)} />)}
      <ArtifactToolSeparator />
      <ArtifactTool icon={ZoomOut} label="Zoom out" disabled={zoom === APP_ZOOMS[0]} onClick={() => setZoom(current => stepZoom(current, -1))} />
      <button type="button" aria-label="Fit width" title="Fit width" aria-pressed={zoom === "fit"} onClick={() => setZoom("fit")}
        className="h-[30px] min-w-[3rem] rounded-[var(--radius-row)] px-1.5 font-mono text-xs text-muted-foreground hover:bg-accent hover:text-foreground coarse:min-h-[44px]">{zoom === "fit" ? "Fit" : `${zoom} %`}</button>
      <ArtifactTool icon={ZoomIn} label="Zoom in" disabled={zoom === APP_ZOOMS[APP_ZOOMS.length - 1]} onClick={() => setZoom(current => stepZoom(current, 1))} />
    </ArtifactSlot>
    {parts.select.available && <ArtifactSlot name="tools" label="Comment tools">
      <ArtifactTool icon={MessageSquarePlus} label={parts.select.on ? "Stop selecting" : "Add a comment"} text={phone ? undefined : "Comment"} on={parts.select.on} onClick={parts.select.toggle} />
      <span className="px-2 text-xs text-muted-foreground tabular-nums">{data.comments.length} {data.comments.length === 1 ? "comment" : "comments"}{parts.waiting > 0 ? ` · ${parts.waiting} waiting` : ""}</span>
    </ArtifactSlot>}
    <ProjectDrawer parts={parts} />
  </div>
}
