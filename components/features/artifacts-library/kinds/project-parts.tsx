"use client"

import * as React from "react"
import { ChevronDown, FileCode2, History, Maximize2, MousePointerClick } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { ArtifactAgentMode, ArtifactDrawerSection, ArtifactTool, useArtifactFrame, type FrameStatus } from "../frame"

/** What a kind body tells the detail about the open artifact: its chip and whether this person may change it. */
export interface KindReport {
  status: FrameStatus | null
  canEdit: boolean
}

/** Reports to the detail while mounted and clears the report when the body leaves. */
export function useKindReport(onReport: ((report: KindReport | null) => void) | undefined, report: KindReport) {
  const key = JSON.stringify(report)
  React.useEffect(() => { onReport?.(JSON.parse(key) as KindReport) }, [onReport, key])
  React.useEffect(() => () => onReport?.(null), [onReport])
}

/** The version picker of the view slot: Latest and Shared, the shown older one, and the full history in the drawer. */
export function VersionMenu({ parts }: { parts: ArtifactViewerParts }) {
  const frame = useArtifactFrame()
  const { latest, shared, shown, viewingOld } = parts.version
  const label = `v${shown} · ${!viewingOld ? "Latest" : shown === shared ? "Shared" : "Older"}`
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <ArtifactTool icon={History} label={`Version: ${label}`} text={<span className="inline-flex items-center gap-1 font-mono text-xs">{label}<ChevronDown aria-hidden className="size-3" /></span>} />
    </DropdownMenuTrigger>
    <DropdownMenuContent align="start" className="w-60">
      <DropdownMenuLabel className="text-xs text-muted-foreground">Versions</DropdownMenuLabel>
      <DropdownMenuItem onSelect={() => parts.version.pick("latest")} data-on={!viewingOld}>Latest · v{latest}</DropdownMenuItem>
      {shared > 0 && shared !== latest && <DropdownMenuItem onSelect={() => parts.version.pick("shared")} data-on={shown === shared}>Shared · v{shared}<small className="ml-auto text-xs text-muted-foreground">What readers see</small></DropdownMenuItem>}
      {shared === 0 && <DropdownMenuItem disabled>No version is shared yet</DropdownMenuItem>}
      {viewingOld && shown !== shared && <DropdownMenuItem data-on>v{shown}</DropdownMenuItem>}
      {parts.history && <>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => frame?.openDrawer("history")}><History aria-hidden />Version history</DropdownMenuItem>
      </>}
    </DropdownMenuContent>
  </DropdownMenu>
}

/** The page or file picker of a multi-file project. */
export function ScreenMenu({ parts }: { parts: ArtifactViewerParts }) {
  if (parts.screens.length < 2) return null
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <ArtifactTool icon={FileCode2} label={`Page: ${parts.labelOf(parts.screen)}`} text={<span className="inline-flex max-w-[12rem] items-center gap-1"><span className="truncate">{parts.labelOf(parts.screen)}</span><ChevronDown aria-hidden className="size-3 shrink-0" /></span>} />
    </DropdownMenuTrigger>
    <DropdownMenuContent align="start" className="w-56">
      <DropdownMenuLabel className="text-xs text-muted-foreground">{parts.screens.length} pages</DropdownMenuLabel>
      {parts.screens.map(path => <DropdownMenuItem key={path} onSelect={() => parts.setScreen(path)} data-on={path === parts.screen} title={path}>{parts.labelOf(path)}</DropdownMenuItem>)}
    </DropdownMenuContent>
  </DropdownMenu>
}

/** Full screen, only where the browser offers it. */
export function FullScreenTool({ target }: { target: React.RefObject<HTMLElement | null> }) {
  const [supported, setSupported] = React.useState(false)
  React.useEffect(() => { setSupported(typeof document !== "undefined" && document.fullscreenEnabled === true) }, [])
  if (!supported) return null
  return <ArtifactTool icon={Maximize2} label="Open full screen" onClick={() => { void target.current?.requestFullscreen?.().catch(() => {}) }} />
}

/**
 * The drawer side of a project: the comment queue under Comments, the
 * version list under History, and Propose in the Agent tab when the server
 * lets this person send comments to the source agent (send_to_agent and
 * the chat dispatcher, the same path the viewer always used).
 */
export function ProjectDrawer({ parts }: { parts: ArtifactViewerParts }) {
  const frame = useArtifactFrame()
  const startSelecting = () => {
    if (!parts.select.on) parts.select.toggle()
    if (frame && frame.layout !== "desktop") frame.openDrawer(null)
  }
  return <>
    <ArtifactDrawerSection tab="comments" label="Comments">
      <div className="grid content-start">
        <p className="flex items-center gap-2 border-b px-3.5 py-2 text-xs text-muted-foreground">
          {parts.data.comments.length} {parts.data.comments.length === 1 ? "comment" : "comments"}{parts.waiting > 0 ? ` · ${parts.waiting} waiting` : ""}
        </p>
        {parts.queue}
        {parts.sendBar}
        {parts.select.available && <p className="px-3.5 py-2.5 text-xs text-muted-foreground">Pin a comment on the preview with Comment.</p>}
      </div>
    </ArtifactDrawerSection>
    {parts.history && <ArtifactDrawerSection tab="history" label="History" icon={History}><div className="p-3">{parts.history}</div></ArtifactDrawerSection>}
    {parts.sendBar && <ArtifactAgentMode mode="propose">
      <div className="grid gap-2.5 text-sm">
        <p className="text-muted-foreground">Select a part of the preview and say what should change. The comment waits in the queue; the agent starts only when you send it, and each change becomes a new version.</p>
        {parts.select.available && <Button type="button" size="sm" variant="secondary" className="justify-self-start coarse:min-h-[44px]" onClick={startSelecting}>
          <MousePointerClick aria-hidden />Select a part
        </Button>}
        <div className="rounded-card border">{parts.sendBar}</div>
      </div>
    </ArtifactAgentMode>}
  </>
}
