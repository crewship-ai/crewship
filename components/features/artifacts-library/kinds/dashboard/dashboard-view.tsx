"use client"

import * as React from "react"
import { ChevronDown, Clock, Database, LayoutDashboard, MessageSquarePlus } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { useArtifactDashboardData } from "@/components/features/chat/artifact-context/projects/use-artifact-dashboard-data"
import { ArtifactDrawerSection, ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "../../frame"
import { FullScreenTool, ProjectDrawer, VersionMenu, useKindReport, type KindReport } from "../project-parts"
import { RuntimeGate } from "../app/app-view"
import { runtimeMissing } from "../app/app-status"
import { parseDashboardDatasets } from "@/lib/pages/artifact-dashboard"
import { DATASET_MODE_LABEL, dashboardFrameStatus, dashboardFreshness, freshnessLabel, freshnessTime, maxAgeLabel, type DashboardFreshness } from "./freshness"

/**
 * Per collection: the author's title, description, mode and maximum age from
 * datasets.json, then what the server confirmed: how many values, the last
 * successful write and who wrote them. `detailed` adds the lineage line.
 */
export function SourcesList({ freshness, codeVersion, detailed = false }: { freshness: DashboardFreshness; codeVersion?: number; detailed?: boolean }) {
  return <>
    {freshness.details === "invalid" && <p role="alert" className="mx-3.5 my-2 rounded-[var(--radius-row)] bg-warn/10 px-2.5 py-1.5 text-xs">The dataset details in datasets.json are invalid, so none are shown. The data itself is unaffected.</p>}
    <ul className="grid">
      {freshness.collections.map(row => <li key={row.name} data-dataset={row.name} className="grid gap-0.5 border-t px-3.5 py-2.5 first:border-t-0">
        <span className="flex items-center gap-2 text-sm"><Database aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
          <b className={cn("min-w-0 truncate font-semibold", !row.dataset?.title && "font-mono")}>{row.dataset?.title ?? row.name}</b>
          <span className="ml-auto shrink-0 text-xs tabular-nums text-muted-foreground">{row.documents} {row.documents === 1 ? "value" : "values"}</span></span>
        {detailed && row.dataset?.description && <p className="whitespace-pre-line text-xs">{row.dataset.description}</p>}
        {detailed && row.dataset && <small className="flex flex-wrap gap-x-2 text-xs">
          <span>{DATASET_MODE_LABEL[row.dataset.mode]}</span>
          {row.dataset.maxAgeSeconds && <span className="text-muted-foreground">· Max age {maxAgeLabel(row.dataset.maxAgeSeconds)}</span>}
          {row.dataset.sample && <span className="font-medium text-warn">· Sample data</span>}
          {row.stale && <span className="font-medium text-warn">· Stale</span>}
        </small>}
        <small className="text-xs text-muted-foreground">
          {row.updatedAt ? `Last write ${freshnessTime(row.updatedAt)}` : "Nothing written yet"}{row.sources.length ? ` · ${row.sources.join(", ")}` : ""}
        </small>
        {detailed && <small className="font-mono text-xs text-muted-foreground">Collection {row.name}{codeVersion ? ` · declared by v${codeVersion}` : ""}</small>}
      </li>)}
    </ul>
  </>
}

const CLOCK_MS = 30_000
function useMinuteClock(): Date {
  const [now, setNow] = React.useState(() => new Date())
  React.useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), CLOCK_MS)
    return () => window.clearInterval(timer)
  }, [])
  return now
}

/**
 * One Dashboard in the frame (Harbor 2 v87): its own pages as tabs in view,
 * freshness and full screen in output, Sources, Comments and History in the
 * drawer. The chip follows the data read. The dashboard HTML is the
 * author's, drawn in an isolated frame, so on a phone it fills the width as
 * written; the host does not re-flow its KPIs or tables.
 */
export function DashboardProjectView({ workspaceId, parts, onReport }: { workspaceId: string; parts: ArtifactViewerParts; onReport?: (report: KindReport | null) => void }) {
  const frame = useArtifactFrame()
  const phone = frame?.layout === "phone"
  const stage = React.useRef<HTMLDivElement>(null)
  const { data, version } = parts
  // The same read (and cache entry) the preview draws from; nothing is fetched twice.
  const read = useArtifactDashboardData({ workspaceId, pageId: data.page_id, codeVersion: version.shown, files: data.manifest.files })
  const datasets = React.useMemo(() => parseDashboardDatasets(data.manifest.files, read.manifest?.collections ?? []), [data.manifest.files, read.manifest])
  // An unchanged poll keeps the same collections, so a clock re-derives Stale while the dashboard stays open.
  const now = useMinuteClock()
  const freshness = React.useMemo(() => read.collections ? dashboardFreshness(read.collections, datasets, now) : null, [read.collections, datasets, now])
  // Host bindings (data-field) attribute each value; an interactive page draws its own numbers, so no per-value citation exists there.
  // Decided by what is drawn: an interactive project in comment-select mode shows the bound preview, with citations.
  const citations = parts.previewKind === "dashboard"
  const error = Boolean(read.error)
  useKindReport(onReport, {
    status: dashboardFrameStatus({ pending: read.pending, error, limitExceeded: read.limitExceeded, freshness, viewingOld: version.viewingOld, shown: version.shown, now }),
    canEdit: parts.canWrite && parts.maySend,
  })
  const gate = parts.previewKind === "interactive" && runtimeMissing(data.interactive)
  const label = error ? "Data unavailable" : freshness ? freshnessLabel(freshness) : "Loading data…"

  return <div ref={stage} data-dashboard-stage className={cn("@container flex min-h-0 flex-1 flex-col bg-muted", phone ? "pb-28 pt-14" : "px-4 pb-6 pt-16")}>
    <div className={cn("grid w-full gap-2 empty:hidden", phone ? "px-2" : "mx-auto max-w-6xl")}>{parts.banners}{parts.select.hint}</div>
    <div className={cn("relative flex min-h-[480px] w-full min-w-0 flex-1 flex-col overflow-hidden bg-white", !phone && "mx-auto mt-2 max-w-6xl rounded-lg border shadow-sm")}>
      <div ref={parts.canvasRef} className="relative min-h-0 flex-1">{gate ? <RuntimeGate /> : parts.preview}</div>
      {parts.pinCard}
    </div>

    <ArtifactSlot name="view" label="View">
      {parts.screens.length > 1
        ? parts.screens.map(path => <ArtifactTool key={path} icon={LayoutDashboard} label={parts.labelOf(path)} text={parts.labelOf(path)} on={path === parts.screen} onClick={() => parts.setScreen(path)} />)
        : <ArtifactTool icon={LayoutDashboard} label="Overview" text="Overview" on />}
      {(data.versions.length > 1 || version.viewingOld) && <><ArtifactToolSeparator /><VersionMenu parts={parts} /></>}
      {parts.share}
    </ArtifactSlot>
    <ArtifactSlot name="output" label="Output">
      <Popover>
        <PopoverTrigger asChild>
          <ArtifactTool icon={Clock} label={`Data freshness: ${label}`} text={<span className="inline-flex items-center gap-1">{label}<ChevronDown aria-hidden className="size-3" /></span>} />
        </PopoverTrigger>
        <PopoverContent align="end" className="w-72 p-0" aria-label="Data freshness">
          <p className="border-b px-3.5 py-2.5 text-xs text-muted-foreground">Values are read from this dashboard&apos;s shared data every few seconds while it is open.</p>
          {freshness ? <SourcesList freshness={freshness} /> : <p className="px-3.5 py-2.5 text-sm text-muted-foreground">{label}</p>}
        </PopoverContent>
      </Popover>
      <FullScreenTool target={stage} />
    </ArtifactSlot>
    {parts.select.available && <ArtifactSlot name="tools" label="Comment tools">
      <ArtifactTool icon={MessageSquarePlus} label={parts.select.on ? "Stop selecting" : "Add a comment"} text={phone ? undefined : "Comment"} on={parts.select.on} onClick={parts.select.toggle} />
    </ArtifactSlot>}
    {freshness && <ArtifactDrawerSection tab="sources" label="Sources" icon={Database} order={0}>
      <p className="px-3.5 pb-1 pt-3 text-xs text-muted-foreground">{citations
        ? "Each bound value carries its source; hover or focus a number in the dashboard to see it."
        : parts.previewKind === "interactive"
          ? "This interactive preview manages its own rendering, so values carry no per-number citation. The list shows declared datasets and their stored values."
          : "This preview has no per-number citation. The list shows declared datasets and their stored values."}</p>
      <SourcesList freshness={freshness} codeVersion={version.shown} detailed />
    </ArtifactDrawerSection>}
    <ProjectDrawer parts={parts} />
  </div>
}
