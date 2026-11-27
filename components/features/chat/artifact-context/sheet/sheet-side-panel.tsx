"use client"
import { SheetIcon } from "./sheet-icon"
import { useCallback, useEffect, useRef, useState, type MutableRefObject } from "react"
import { Maximize2, Minimize2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { HintTooltip } from "@/components/ui/hint-tooltip"
import { usePage } from "@/hooks/use-pages"
import { useUnsavedNavigationGuard } from "@/hooks/use-unsaved-navigation-guard"
import { ArtifactNativeEditor } from "@/components/features/artifacts-library/native-editor"
import type { NativeCraftHandBack, NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"
import { artifactsItemHref } from "../artifacts-href"
import { confirmLeaveUnsavedSheet } from "./confirm-unsaved-sheet"

/** After this long without the editor being ready, say that a large Sheet takes a while. */
const SLOW_MS = 5000
/** From this many seconds a save says that a large workbook takes a while (a large one can take about half a minute). */
const SLOW_SAVE_S = 10

/** What a state means, in one sentence, for the states a person may not read at once. */
function stateHint(status: NativeCraftHostStatus | undefined, state: string): string | undefined {
  if (state.startsWith("Viewing")) return status?.version ? `You are looking at saved version ${status.version}. Click a cell to start editing.` : "You are looking at the saved workbook. Click a cell to start editing."
  if (state === "Conflict") return "The workbook changed meanwhile. Your edits are kept here; nothing is lost."
  return undefined
}

/** Whether the person's work is safe, in words. "Saved" only from a confirmed save. */
/** A revision is named only once the server acknowledged it; never while a save is on its way. */
function saveState(status: NativeCraftHostStatus | undefined): string {
  if (!status?.editing) return status?.version ? `Viewing · version ${status.version}` : "Viewing"
  if (status.conflict) return "Conflict"
  if (status.phase === "uncertain") return "Save not confirmed"
  if (status.phase === "saving") return "Saving…"
  if (status.dirty) return "Not saved yet"
  return status.savedVersion ? `Saved · version ${status.savedVersion}` : "Editing"
}

/**
 * A native Sheet beside the chat: the same single-editor host the Artifacts
 * detail mounts, with its own lock, save and recovery. It stays mounted while
 * another pane is shown or the chat goes on (opening a large Sheet takes long),
 * and is unmounted only when the person closes it, after a confirmation when
 * something is unsaved. While the agent holds the lock the host shows the
 * Sheet read-only and says so; nothing here takes or releases a lock.
 */
export function SheetSidePanel({ workspaceId, pageId, hidden, expanded, onExpandedChange, onClose, dirtyRef, handBackRef, onStatus, agentPending, opener, realtimeChatId }: {
  realtimeChatId?: string
  workspaceId: string
  pageId: string
  hidden: boolean
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  onClose: () => void
  /** Kept current for the chat, which asks before it replaces this Sheet with another. */
  dirtyRef?: MutableRefObject<boolean>
  /** Filled with the way to give the person's edit access back before a message lets the agent change the Sheet. */
  handBackRef?: MutableRefObject<(() => Promise<NativeCraftHandBack>) | null>
  onStatus?: (status: NativeCraftHostStatus | undefined) => void
  /** A message let the agent change this Sheet and the agent has not taken it yet. */
  agentPending?: boolean
  /** What the person pressed to open this Sheet: the keyboard moves into the panel only while it is still there (never from where they type). */
  opener?: Element | null
}) {
  const { page } = usePage(workspaceId, `@${pageId}`)
  const [status, setStatus] = useState<NativeCraftHostStatus>()
  const prepareLeaveRef = useRef<(() => Promise<boolean>) | null>(null)
  const prepareLeave = useCallback(() => prepareLeaveRef.current?.() ?? Promise.resolve(!realtimeChatId), [realtimeChatId])
  useUnsavedNavigationGuard(Boolean(status?.holderIsMe || status?.dirty), "", prepareLeave)
  useEffect(() => { if (dirtyRef) dirtyRef.current = Boolean(status?.dirty) }, [dirtyRef, status?.dirty])
  useEffect(() => () => { if (dirtyRef) dirtyRef.current = false }, [dirtyRef])
  const statusSink = useRef(onStatus)
  statusSink.current = onStatus
  useEffect(() => { statusSink.current?.(status) }, [status])
  useEffect(() => () => { statusSink.current?.(undefined) }, [])
  const close = useCallback(async () => {
    if (realtimeChatId && status?.dirty && prepareLeaveRef.current && await prepareLeave()) { onClose(); return }
    if (status?.dirty && !(await confirmLeaveUnsavedSheet())) return
    onClose()
  }, [status?.dirty, onClose, prepareLeave, realtimeChatId])
  // A large Sheet takes long to open: say so once it has, and keep the panel alive.
  const ready = Boolean(status?.ready)
  const [slow, setSlow] = useState(false)
  useEffect(() => {
    if (ready) { setSlow(false); return }
    const timer = setTimeout(() => setSlow(true), SLOW_MS)
    return () => clearTimeout(timer)
  }, [ready])
  // A save of a large workbook recalculates it on the server: after a few seconds say so, so it never looks frozen.
  const saving = status?.phase === "saving"
  // Editing goes on meanwhile; the line counts the seconds, so a long save never looks frozen.
  const [savingFor, setSavingFor] = useState(0)
  useEffect(() => {
    setSavingFor(0)
    if (!saving) return
    const started = Date.now()
    const timer = setInterval(() => setSavingFor(Math.floor((Date.now() - started) / 1000)), 1000)
    return () => clearInterval(timer)
  }, [saving])
  const savingLine = savingFor < 1 ? "Saving…" : `Saving… ${savingFor} s${savingFor >= SLOW_SAVE_S ? " · A large workbook can take up to half a minute to save." : ""}`
  const headingRef = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    if (hidden) return
    const active = document.activeElement
    if (!active || active === document.body || (opener && active === opener)) headingRef.current?.focus()
  }, [hidden]) // eslint-disable-line react-hooks/exhaustive-deps -- each time it is shown
  const title = page?.name || "Sheet"
  const realtime=Boolean(realtimeChatId)
  const agentHolds = !realtime && status?.holder?.kind === "agent" && !status.holderIsMe
  // The host's bar (who edits, the saved-values badge, the menu) renders into this element.
  const [bar, setBar] = useState<HTMLDivElement | null>(null)
  const liveState = !ready ? (status?.dirty ? "Not saved" : "Opening…")
    : status?.conflict || status?.phase === "draft" || status?.phase === "uncertain" ? "Not saved"
    : status?.dirty || saving ? "Saving…"
    : status?.editing ? "Saved" : "Read only"
  const state = realtime ? liveState : agentHolds ? "The agent is changing the Sheet…" : agentPending && ready ? "Waiting for the agent…" : !ready ? (slow ? "Calculating the workbook… a large one can take a minute." : "Opening the Sheet…") : saving && status?.editing ? savingLine : saveState(status)
  const hint = realtime ? undefined : stateHint(status, state)
  const tone = !ready || saving || agentHolds || agentPending ? "busy" : status?.conflict || status?.editing && (status.dirty || status.phase === "uncertain") ? "warn" : status?.editing && status.savedVersion ? "ok" : "plain"
  return <section aria-label="Sheet editor" hidden={hidden} data-testid="sheet-side-panel" data-expanded={expanded}
    className="flex h-full min-w-0 flex-col overflow-hidden border-l bg-background"
    onKeyDown={event => { if (event.key === "Escape" && !event.defaultPrevented && !status?.dirty) { event.preventDefault(); event.stopPropagation(); void close() } }}>
    {/* Two rows that never get wider than the panel: close, name, save state (in words, readable without
        colour) and expand; then who edits, the actions and the menu. */}
    <header data-testid="sheet-panel-header" className="flex shrink-0 flex-col gap-1 overflow-x-hidden border-b border-t-2 border-t-sheet px-2 py-1.5">
      <div data-testid="sheet-panel-title-row" className="flex min-w-0 items-center gap-1.5">
        <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" aria-label="Close Sheet" onClick={() => void close()}><X className="size-4" aria-hidden /></Button>
        <SheetIcon size="sm" />
        <h2 ref={headingRef} tabIndex={-1} className="min-w-0 flex-1 truncate text-sm font-semibold outline-none">{title}</h2>
        {(() => {
          const badge = <span role="status" data-testid="sheet-panel-state" data-editing={Boolean(status?.editing)} data-dirty={Boolean(status?.dirty)} data-version={status?.version} data-tone={tone} title={hint ? undefined : state} tabIndex={hint ? 0 : undefined}
            className="min-w-0 max-w-[50%] shrink truncate rounded-full border px-2 py-0.5 text-xs text-muted-foreground data-[tone=warn]:border-warn/50 data-[tone=warn]:text-foreground">{state}</span>
          return hint ? <HintTooltip hint={hint}>{badge}</HintTooltip> : badge
        })()}
        <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" aria-label={expanded ? "Show chat alongside" : "Expand Sheet"} aria-pressed={expanded} onClick={() => onExpandedChange(!expanded)}>
          {expanded ? <Minimize2 className="size-4" aria-hidden /> : <Maximize2 className="size-4" aria-hidden />}
        </Button>
      </div>
      <div ref={setBar} data-testid="sheet-panel-bar" className="flex min-w-0 flex-wrap items-center gap-1.5 empty:hidden" />
    </header>
    {/* Always there and always the same height: only its look changes while something is in progress. */}
    <div aria-hidden data-testid="sheet-panel-progress" data-active={!ready || saving || agentHolds || Boolean(agentPending)}
      className="h-0.5 shrink-0 overflow-hidden bg-transparent data-[active=true]:bg-primary/15">
      <div className="h-full w-full bg-primary opacity-0 transition-opacity [[data-active=true]>&]:opacity-60 motion-safe:[[data-active=true]>&]:animate-pulse" />
    </div>
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-auto">
      <ArtifactNativeEditor realtimeChatId={realtimeChatId} workspaceId={workspaceId} pageId={pageId} fallback={null} onStatus={setStatus} handBackRef={handBackRef} prepareLeaveRef={prepareLeaveRef}
        toolbar={bar} historyHref={artifactsItemHref(workspaceId, pageId)} />
    </div>
  </section>
}
