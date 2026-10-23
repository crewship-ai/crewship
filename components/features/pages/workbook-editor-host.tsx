"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { useRealtimeChannelSafe, useRealtimeEventSafe, useRealtimeStatusSafe, type RealtimeEventType } from "@/hooks/use-realtime"
import { initialSaveState, losesWork, saveReducer, type SaveState } from "@/lib/editor-lifecycle/save-state"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { BRIDGE_MAX_PENDING, EDITOR_FRAME_ATTRIBUTES, createWorkbookBridge, type CellRef, type DraftConflict, type FrameWindow, type KillReason, type Refusal, type RefusalReason, type WorkbookBridge } from "@/lib/workbooks/bridge"
import { WORKBOOK_COMMITTED_EVENT, WORKBOOK_SESSION_REOPEN_MAX_MS, createWorkbookEditorClient, editorFrameSrc, parseCommittedPush, type SessionCheck, type WorkbookEditorClient } from "@/lib/workbooks/editor-client"
import { PageWorkbook } from "./page-workbook"

/** How often the host reads the head revision while the editor is visible and no realtime push can arrive. */
export const WORKBOOK_HEAD_POLL_MS = 2000
/** The same read while the realtime connection is up: `workbook.committed` drives it, this is the fallback. */
export const WORKBOOK_HEAD_POLL_CONNECTED_MS = 15_000
/**
 * Session check while the editor is open and visible. It renews the session (15 minutes idle), and
 * is also how the host learns soon that the editor was turned off or its bundle revoked. The server
 * writes a renewal only once less than 10 minutes remain and otherwise just answers the current
 * expiry, so checking every minute stays cheap; a hidden tab does not check and its session idles out.
 */
export const WORKBOOK_SESSION_HEARTBEAT_MS = 60_000
/** Least wait before opening the editor again by itself after `429 too_many_editor_sessions`. */
export const WORKBOOK_SESSION_REOPEN_MIN_MS = 30_000
/** New frames opened in a row after the workbook moved to a new generation. */
export const WORKBOOK_GENERATION_REOPENS = 3
/** Least time between two rechecks of the open session (focus, reconnect, back online). */
export const WORKBOOK_SESSION_RECHECK_MS = 5000

/** Why the editor is not (or no longer) showing. `closed` is an unmount and never reaches the UI. */
export type EditorEnd =
  | { reason: Exclude<KillReason, "closed">; unsaved: number }
  /**
   * The server refused or failed to open a session (`status` 0: network or unreadable). After `429`
   * (too many open sessions) `retryAfterMs` says when one is likely free again.
   */
  | { reason: "unavailable"; status: number; retryAfterMs?: number }
  /** The session named an editor URL the host may not frame, or the browser cannot isolate it. */
  | { reason: "config" }

/**
 * The browser can load the editor credentialless. Without that attribute support a framed editor
 * would carry this site's cookies to the editor origin, so the editor is Chromium-only (ADR §9) and
 * every other browser keeps the read-only preview.
 */
export function editorFrameSupported(): boolean {
  return typeof HTMLIFrameElement !== "undefined" && "credentialless" in HTMLIFrameElement.prototype
}

/**
 * Creates the editor iframe with exactly `EDITOR_FRAME_ATTRIBUTES`, set as attributes (React
 * would drop the unknown boolean `credentialless`) and verified before `src` is assigned, so no
 * document ever loads in a frame without them. `onLoad` hears every load event, the first one
 * included. Returns null when the attributes did not stick.
 */
export function createEditorFrame(doc: Document, src: string, onLoad: () => void): HTMLIFrameElement | null {
  const frame = doc.createElement("iframe")
  frame.setAttribute("sandbox", EDITOR_FRAME_ATTRIBUTES.sandbox)
  if (EDITOR_FRAME_ATTRIBUTES.credentialless) frame.setAttribute("credentialless", "")
  frame.setAttribute("referrerpolicy", EDITOR_FRAME_ATTRIBUTES.referrerPolicy)
  frame.setAttribute("loading", "eager")
  frame.setAttribute("title", "Workbook editor")
  frame.className = "absolute inset-0 h-full w-full border-0 bg-background"
  frame.addEventListener("load", onLoad)
  const tokens = (frame.getAttribute("sandbox") ?? "").split(/\s+/).filter(Boolean)
  if (tokens.length !== 1 || tokens[0] !== "allow-scripts") return null
  if (!frame.hasAttribute("credentialless") || frame.getAttribute("referrerpolicy") !== "no-referrer") return null
  // Where the browser reflects it, the property must agree with the attribute.
  if ("credentialless" in frame && (frame as { credentialless?: unknown }).credentialless !== true) return null
  frame.src = src
  return frame
}

/** The one save status line. "Saved" only when a server receipt covers every edit. */
export function workbookSaveLabel(state: SaveState): string {
  if (!state.opened) return "Connecting…"
  if (!state.canWrite || state.trouble === "denied") return "Access lost"
  // A refused change waits for the person's decision: never "Saved" or "No unsaved changes" meanwhile.
  const refused = state.refused === 1 ? "a change was refused" : `${state.refused} changes were refused`
  if (state.pending > 0) {
    const unsent = `${state.pending} ${state.pending === 1 ? "change" : "changes"} not saved`
    if (state.trouble === "offline") return `Offline · ${unsent}`
    if (state.trouble === "stuck") return `Not saved · server not answering · ${unsent}`
    if (state.trouble === "retrying" || state.trouble === "rate-limited") return "Saving… · retrying"
    if (state.trouble === "rejected" || state.trouble === "unconfirmed") return `Not saved · ${unsent}`
    return state.refused > 0 ? `Saving… · ${refused}` : "Saving…"
  }
  if (state.refused > 0) return state.trouble === "unconfirmed" ? "Not saved · the server did not confirm a change" : `Not saved · ${refused}`
  if (state.receipt) return "Saved"
  if (state.trouble === "rejected" || state.trouble === "conflict") return "Not saved · a change was refused"
  if (state.trouble === "unconfirmed") return "Not saved · the server did not confirm a change"
  return "No unsaved changes"
}

/** When the host opens the editor again by itself after `429`: the server's wait, within bounds. */
function reopenDelay(end: EditorEnd | undefined): number | null {
  if (end?.reason !== "unavailable" || end.status !== 429) return null
  return Math.min(Math.max(end.retryAfterMs ?? 0, WORKBOOK_SESSION_REOPEN_MIN_MS), WORKBOOK_SESSION_REOPEN_MAX_MS)
}

function endMessage(end: EditorEnd): { text: string; reopen: boolean } {
  const unsaved = "unsaved" in end && end.unsaved > 0 ? ` · ${end.unsaved} ${end.unsaved === 1 ? "change" : "changes"} not saved` : ""
  const delay = reopenDelay(end)
  if (delay !== null) {
    const minutes = Math.max(1, Math.ceil(delay / 60_000))
    return { text: `Too many editor windows are open for this workbook. Close one and open the editor again, or it opens by itself in about ${minutes} ${minutes === 1 ? "minute" : "minutes"}. Showing the read-only preview.`, reopen: true }
  }
  switch (end.reason) {
    case "unavailable": return { text: end.status === 503 ? "The workbook editor is turned off. Showing the read-only preview." : "The workbook editor is unavailable. Showing the read-only preview.", reopen: end.status === 0 || end.status >= 500 }
    case "config": return { text: "The workbook editor cannot be opened safely here. Try current Chrome or Edge at this installation’s canonical address. If it still fails, ask an administrator to check the editor origin and approved bundle. Showing the read-only preview.", reopen: false }
    case "access": return { text: `Access lost${unsaved}`, reopen: false }
    case "disabled": return { text: `The workbook editor is turned off${unsaved}. Showing the read-only preview.`, reopen: false }
    case "expired": return { text: `The editor session ended${unsaved}. Open the editor again to continue.`, reopen: true }
    case "load": return { text: `The workbook editor could not load the workbook${unsaved}. Showing the read-only preview.`, reopen: true }
    case "bundle": return { text: `The editor bundle did not match the approved version${unsaved}. Showing the read-only preview.`, reopen: false }
    case "timeout": return { text: "The workbook editor did not start. Showing the read-only preview.", reopen: true }
    case "generation": return { text: `This workbook changed version. Open the editor again to continue${unsaved}.`, reopen: true }
    default: return { text: `The editor was closed because its frame broke the protocol${unsaved}. Showing the read-only preview.`, reopen: true }
  }
}

/** An early warning on screen: another writer changed a cell with an open draft. */
export type ShownConflict = { key: string; kind: "changed"; sheet: string; cell: string; input: string; actor_label: string }

const conflictCell = (sheet: string, cell: string) => `${sheet}!${cell}`

/** The warnings to show after the bridge reported one. A newer report for a cell replaces the older one. */
export function addConflict(shown: ShownConflict[], conflict: DraftConflict, key: string): ShownConflict[] {
  const cell = conflictCell(conflict.sheet, conflict.cell)
  return [...shown.filter(item => conflictCell(item.sheet, item.cell) !== cell), { key, kind: "changed", sheet: conflict.sheet, cell: conflict.cell, input: conflict.input, actor_label: conflict.actor_label }]
}

/** Early warnings stay only while their cell still has an open draft in the editor. */
export function keepOpenDrafts(shown: ShownConflict[], drafts: CellRef[]): ShownConflict[] {
  const open = new Set(drafts.map(ref => conflictCell(ref.sheet, ref.cell)))
  const next = shown.filter(item => open.has(conflictCell(item.sheet, item.cell)))
  return next.length === shown.length ? shown : next
}

/** A refusal replaces the early warning for its cells (the draft was committed). */
function withoutRefused(shown: ShownConflict[], refusals: Refusal[]): ShownConflict[] {
  const refused = new Set(refusals.flatMap(refusal => refusal.cells.map(cell => conflictCell(cell.sheet, cell.cell))))
  const next = shown.filter(item => !refused.has(conflictCell(item.sheet, item.cell)))
  return next.length === shown.length ? shown : next
}

/** Cells as one label: `Sheet1!B2, C2, Sheet2!A1` (the sheet named when it changes). */
export function cellList(cells: { sheet: string; cell: string }[]): string {
  return cells.map((cell, index) => index > 0 && cells[index - 1].sheet === cell.sheet ? cell.cell : conflictCell(cell.sheet, cell.cell)).join(", ")
}

const REFUSAL_TEXT: Record<RefusalReason, string> = {
  precondition_failed: "the workbook changed underneath this change",
  conflict: "the workbook changed underneath this change",
  invalid_ops: "the server could not accept these cells",
  history_quota_exceeded: "the workbook's history is full",
  too_many_pending: "too many changes were waiting to be saved",
  not_confirmed: "the server did not confirm it",
  effect_out_of_mandate: "its recalculation could not be confirmed",
  reloaded: "the workbook was reloaded before your choice was saved",
}

/** Shown on a row whose decision could not be queued because the save queue is full. */
export const QUEUE_FULL_TEXT = "Too many changes are waiting to be saved. Try again once they are saved."

function RefusalRow({ refusal, queueFull, onKeepMine, onTakeTheirs }: { refusal: Refusal; queueFull: boolean; onKeepMine: (id: number) => void; onTakeTheirs: (id: number) => void }) {
  const conflicting = refusal.cells.filter(cell => cell.theirs !== undefined)
  const rest = refusal.cells.filter(cell => cell.theirs === undefined)
  const sending = refusal.state === "sending"
  const take = conflicting.length
    ? `Take theirs for ${cellList(conflicting)}${rest.length ? ` and save ${cellList(rest)}` : ""}`
    : `Discard mine for ${cellList(refusal.cells)}`
  // Focus goes to the row itself after a decision (tabIndex -1), never to its buttons: a repeated
  // Enter must not decide the next refusal too.
  return <div data-kind="refused" data-state={refusal.state} tabIndex={-1} className="flex flex-wrap items-center gap-2 outline-none focus-visible:ring-2 focus-visible:ring-ring">
    {/* Each row announces itself once, politely; the buttons stay out of the live region. */}
    <span role="status" aria-live="polite" className="mr-auto">
      {!conflicting.length && <span className="block">Not saved: {REFUSAL_TEXT[refusal.reason]}.</span>}
      {refusal.cells.map(cell => <span key={conflictCell(cell.sheet, cell.cell)} className="block">
        {cell.theirs !== undefined
          ? <>Changed by {cell.actor_label} while you were editing: {conflictCell(cell.sheet, cell.cell)}. Yours: <q className="break-all">{cell.mine}</q> · Theirs: <q className="break-all">{cell.theirs}</q></>
          : <>Not saved yet: {conflictCell(cell.sheet, cell.cell)}. Yours: <q className="break-all">{cell.mine}</q></>}
      </span>)}
      {conflicting.length > 0 && rest.length > 0 && <span className="block text-muted-foreground">Take theirs keeps their value for {cellList(conflicting)} and still saves your other cells.</span>}
      {sending && <span className="block text-muted-foreground">Saving your choice…</span>}
      {queueFull && !sending && <span className="block">{QUEUE_FULL_TEXT}</span>}
    </span>
    {refusal.retryable && <Button type="button" size="sm" disabled={sending} aria-label={`Keep mine for ${cellList(refusal.cells)}`} onClick={() => onKeepMine(refusal.id)}>Keep mine</Button>}
    <Button type="button" size="sm" variant="outline" disabled={sending} aria-label={take} onClick={() => onTakeTheirs(refusal.id)}>{conflicting.length ? "Take theirs" : "Discard mine"}</Button>
  </div>
}

/** Edits refused past the open-refusal bound: the editor alone holds them; the page counts them. */
function OverflowRow({ count, onDiscard }: { count: number; onDiscard: () => void }) {
  const changes = count === 1 ? "1 refused change" : `${count} refused changes`
  return <div data-kind="overflow" tabIndex={-1} className="flex flex-wrap items-center gap-2 outline-none focus-visible:ring-2 focus-visible:ring-ring">
    <span role="status" aria-live="polite" className="mr-auto">
      {count === 1 ? "1 more change was" : `${count} more changes were`} refused because too many were waiting to be saved. Only the editor still shows {count === 1 ? "it" : "them"}; they are not saved.
    </span>
    <Button type="button" size="sm" variant="outline" aria-label={`Discard ${changes} held in the editor`} onClick={onDiscard}>Discard them</Button>
  </div>
}

function ConflictNotice({ conflicts, refusals, overflow, queueFull, onKeepMine, onTakeTheirs, onDiscardOverflow, panel }: { conflicts: ShownConflict[]; refusals: Refusal[]; overflow: number; queueFull: ReadonlySet<number>; onKeepMine: (id: number) => void; onTakeTheirs: (id: number) => void; onDiscardOverflow: () => void; panel: React.Ref<HTMLDivElement> }) {
  if (!conflicts.length && !refusals.length && !overflow) return null
  return <div ref={panel} data-testid="workbook-draft-conflict" role="region" aria-label="Workbook changes" className="absolute bottom-12 right-3 z-20 flex max-h-[40%] w-[min(36rem,calc(100%-1.5rem))] flex-col gap-2 overflow-y-auto rounded-md border border-warn/50 bg-background px-3 py-2 text-sm shadow-lg">
    {conflicts.map(item => <p key={item.key} data-kind="changed" role="status" aria-live="polite">
      Changed by {item.actor_label} while you were editing: {conflictCell(item.sheet, item.cell)} is now <q className="break-all">{item.input}</q>. Your draft is kept; when you commit it you can keep yours or take theirs.
    </p>)}
    {refusals.map(refusal => <RefusalRow key={refusal.id} refusal={refusal} queueFull={queueFull.has(refusal.id)} onKeepMine={onKeepMine} onTakeTheirs={onTakeTheirs} />)}
    {overflow > 0 && <OverflowRow count={overflow} onDiscard={onDiscardOverflow} />}
  </div>
}

/**
 * A workbook Page: the read-only preview by default, the embedded editor when the workspace flag
 * `gridcraft_editor` is on and the server opens an editor session. Any failure falls back to the
 * preview; nothing about the editor is cached between Pages.
 */
export function PageWorkbookSurface({ workspaceId, pageId, name, onDirtyChange, client }: {
  workspaceId: string
  pageId: string
  name?: string
  onDirtyChange?: (dirty: boolean) => void
  /** Tests pass a fake; the app uses the HTTP client for this Page. */
  client?: WorkbookEditorClient
}) {
  const editorClient = React.useMemo(() => client ?? createWorkbookEditorClient({ workspaceId, pageId }), [client, workspaceId, pageId])
  const [enabled, setEnabled] = React.useState<boolean | null>(null)
  const [flagError, setFlagError] = React.useState(false)
  const [flagAttempt, setFlagAttempt] = React.useState(0)
  const [end, setEnd] = React.useState<EditorEnd>()
  const [attempt, setAttempt] = React.useState(0)
  const reopens = React.useRef(0)

  React.useEffect(() => {
    let live = true
    setEnabled(null)
    setFlagError(false)
    void editorClient.editorEnabled().then(value => { if (live) setEnabled(value) }).catch(() => { if (live) setFlagError(true) })
    return () => { live = false }
  }, [editorClient, flagAttempt])

  const onEnd = React.useCallback((next: EditorEnd) => {
    // A new generation needs a new frame and session; the old frame never sees the new content.
    if (next.reason === "generation" && reopens.current < WORKBOOK_GENERATION_REOPENS) {
      reopens.current++
      setAttempt(value => value + 1)
      return
    }
    setEnd(next)
  }, [])
  const reopen = React.useCallback(() => { reopens.current = 0; setEnd(undefined); setAttempt(value => value + 1) }, [])
  // Too many open sessions: one frees up when it idles out, so try again then, once per refusal.
  React.useEffect(() => {
    const delay = reopenDelay(end)
    if (delay === null) return
    const timer = setTimeout(reopen, delay)
    return () => clearTimeout(timer)
  }, [end, reopen])

  if (enabled === true && !end && editorFrameSupported()) {
    return <WorkbookEditorHost key={attempt} client={editorClient} pageId={pageId} name={name} onEnd={onEnd} onDirtyChange={onDirtyChange} />
  }
  const fallback = flagError
    ? { reason: "flag-error", text: "Could not check workbook editor availability. Check your connection and retry. Showing the read-only preview." }
    : enabled === null
      ? { reason: "checking", text: "Checking editor availability… Showing the read-only preview." }
      : !enabled
        ? { reason: "workspace-disabled", text: "The workbook editor is not enabled for this workspace. Ask a workspace administrator to enable GridCraft editing. Showing the read-only preview." }
        : !editorFrameSupported()
          ? { reason: "unsupported-browser", text: "This browser cannot isolate the workbook editor safely. Open this page in a current version of Chrome or Edge. Showing the read-only preview." }
          : undefined
  const notice = end ? endMessage(end) : fallback && { text: fallback.text, reopen: false }
  return <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
    {notice && <div role={fallback?.reason === "checking" ? "status" : "alert"} data-testid={fallback?.reason === "checking" && !end ? "workbook-editor-checking" : "workbook-editor-notice"} data-reason={end?.reason ?? fallback?.reason} className="flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm">
      <span className="mr-auto">{notice.text}</span>
      {flagError && <Button type="button" size="sm" variant="outline" onClick={() => setFlagAttempt(value => value + 1)}>Retry editor check</Button>}
      {notice.reopen && <Button type="button" size="sm" variant="outline" onClick={reopen}>Open editor again</Button>}
    </div>}
    <PageWorkbook workspaceId={workspaceId} pageId={pageId} name={name} />
  </div>
}

/**
 * The embedded editor: one frame session, one sandboxed frame, one bridge. The frame is created
 * only after the server opened the session, and is removed the moment the bridge ends the session
 * (navigation, broken protocol, lost access, new generation, editor turned off, no handshake in
 * time). Unsent edits hold a leave guard. On focus, reconnect and back online the host renews the
 * session, which rechecks the flag and the qualified bundle.
 */
export function WorkbookEditorHost({ client, pageId, name, onEnd, onDirtyChange }: {
  client: WorkbookEditorClient
  pageId: string
  name?: string
  onEnd: (end: EditorEnd) => void
  onDirtyChange?: (dirty: boolean) => void
}) {
  const container = React.useRef<HTMLDivElement>(null)
  const [save, dispatch] = React.useReducer(saveReducer, initialSaveState)
  const [rev, setRev] = React.useState<number>()
  const [conflicts, setConflicts] = React.useState<ShownConflict[]>([])
  /** The bridge's open refusals: they survive reloads until the person decides each one. */
  const [refusals, setRefusals] = React.useState<Refusal[]>([])
  /** Edits refused past the open-refusal bound, held only by the editor (a count). */
  const [overflow, setOverflow] = React.useState(0)
  /** Refusals whose decision could not be queued (the save queue was full): they say to try again. */
  const [queueFull, setQueueFull] = React.useState<ReadonlySet<number>>(() => new Set())
  const conflictSeq = React.useRef(0)
  const panel = React.useRef<HTMLDivElement>(null)
  /** After a decision: where focus goes once the notice re-rendered. */
  const refocus = React.useRef(false)
  const bridgeRef = React.useRef<WorkbookBridge | undefined>(undefined)
  const pollRef = React.useRef<() => void>(() => {})
  const onEndRef = React.useRef(onEnd); onEndRef.current = onEnd
  const nameRef = React.useRef(name); nameRef.current = name
  const realtimeStatus = useRealtimeStatusSafe()
  const pushesLive = React.useRef(false); pushesLive.current = realtimeStatus === "connected"

  useLeaveGuard(losesWork(save), onDirtyChange)

  React.useEffect(() => {
    let disposed = false
    let bridge: WorkbookBridge | undefined
    let frame: HTMLIFrameElement | null = null
    let timer: ReturnType<typeof setInterval> | undefined
    let heartbeat: ReturnType<typeof setInterval> | undefined
    let polledAt = Date.now()
    let polling = false
    let sessionGeneration = 0
    let checkSession: ((current: WorkbookBridge) => Promise<SessionCheck>) | undefined
    let checking = false, checkedAt = -Infinity

    const onMessage = (event: MessageEvent) => bridge?.handleWindowMessage(event)
    const onLoad = () => bridge?.frameLoaded()
    const online = () => { bridge?.online(); void recheck() }
    const offline = () => bridge?.offline()
    const onFocus = () => { void poll(); void recheck() }
    const detach = () => {
      if (timer !== undefined) clearInterval(timer)
      if (heartbeat !== undefined) clearInterval(heartbeat)
      timer = heartbeat = undefined
      window.removeEventListener("message", onMessage)
      window.removeEventListener("online", online)
      window.removeEventListener("offline", offline)
      document.removeEventListener("visibilitychange", onFocus)
      window.removeEventListener("focus", onFocus)
      if (frame) { frame.removeEventListener("load", onLoad); frame.remove(); frame = null }
    }
    const finish = (end: EditorEnd) => {
      detach()
      if (disposed) return
      disposed = true
      onEndRef.current(end)
    }

    async function poll() {
      const current = bridge
      if (!current || current.phase !== "live" || polling || document.visibilityState === "hidden") return
      polling = true
      polledAt = Date.now()
      try {
        const result = await client.head(current.confirmedRev)
        if (current.phase !== "live") return
        if (result.ok) { current.notify(result.head); setRev(current.confirmedRev); return }
        if (result.refusal) current.close(result.refusal)
        // Beyond the feed's window: the bridge reads the feed, gets 410 and reloads the snapshot.
        else if (result.status === 410) current.notify({ rev: current.confirmedRev + 1, generation: sessionGeneration })
      } finally {
        polling = false
      }
    }
    /** Renews the session: the editor turned off, its bundle revoked or the session over ends it. */
    async function recheck() {
      const current = bridge
      if (!current || !checkSession || current.phase !== "live" || checking || document.visibilityState === "hidden") return
      const now = Date.now()
      if (now - checkedAt < WORKBOOK_SESSION_RECHECK_MS) return
      checking = true; checkedAt = now
      try {
        const checked = current.session.session_id
        const result = await checkSession(current)
        if (current.phase !== "live") return
        if (result === "revoked") current.close("disabled")
        // An expired session is replaced and the pending edits go out under the new one.
        else if (result === "expired") current.sessionExpired(checked)
        else if (result !== "live" && result !== "unknown") current.close(result)
      } finally {
        checking = false
      }
    }
    pollRef.current = () => { void poll(); void recheck() }

    void (async () => {
      const opened = await client.openSession()
      if (disposed) {
        if (opened.ok) void client.transport(opened.session).closeSession().catch(() => {})
        return
      }
      if (!opened.ok) { finish({ reason: "unavailable", status: opened.status, retryAfterMs: opened.retryAfterMs }); return }
      const session = opened.session
      sessionGeneration = session.generation
      // The bridge's session, which changes when an expired one is replaced.
      checkSession = current => client.checkSession(current.session)
      const transport = client.transport(session)
      const src = editorFrameSrc(session.editor_url, session.bundle_digest, window.location.origin)
      if (!src || !editorFrameSupported()) {
        void transport.closeSession().catch(() => {})
        finish({ reason: "config" })
        return
      }
      bridge = createWorkbookBridge({
        session,
        transport,
        frame: () => (frame?.contentWindow ?? null) as FrameWindow | null,
        pageLabel: nameRef.current ?? "Workbook",
        // Display and correlation only, never a credential.
        clientId: typeof crypto.randomUUID === "function" ? crypto.randomUUID() : Math.random().toString(36).slice(2),
        onSaveEvent: event => {
          if (disposed) return
          dispatch(event)
          if (bridge) setRev(bridge.confirmedRev)
        },
        reopenSession: async () => {
          const next = await client.openSession()
          if (!next.ok) return { ok: false, refusal: next.refusal }
          return { ok: true, session: next.session, transport: client.transport(next.session) }
        },
        onConflict: conflict => {
          if (disposed) return
          setConflicts(shown => addConflict(shown, conflict, `c${++conflictSeq.current}`))
        },
        onRefusals: (open, overflowed) => {
          if (disposed) return
          setRefusals(open)
          setOverflow(overflowed)
          setConflicts(shown => withoutRefused(shown, open))
        },
        onDrafts: cells => {
          if (disposed) return
          setConflicts(shown => keepOpenDrafts(shown, cells))
        },
        // The editor holds new content: warnings about what it replaced no longer apply. Open
        // refusals stay: they hold the person's values until the person decides.
        onReload: () => {
          if (disposed) return
          setConflicts([])
        },
        onKill: kill => {
          // The frame goes first: after a kill no document in it may hold anything of this session.
          detach()
          if (kill.reason !== "closed") finish({ reason: kill.reason, unsaved: kill.unsaved })
        },
      })
      bridgeRef.current = bridge
      window.addEventListener("message", onMessage)
      frame = createEditorFrame(document, src, onLoad)
      if (!frame || !container.current) {
        // The browser would not keep the isolation attributes: a configuration problem, not the frame's fault.
        bridge.close("closed")
        finish({ reason: "config" })
        return
      }
      window.addEventListener("online", online)
      window.addEventListener("offline", offline)
      document.addEventListener("visibilitychange", onFocus)
      window.addEventListener("focus", onFocus)
      polledAt = Date.now()
      // With realtime up, pushes drive the feed and the head read is only a slow fallback.
      timer = setInterval(() => { if (!pushesLive.current || Date.now() - polledAt >= WORKBOOK_HEAD_POLL_CONNECTED_MS) void poll() }, WORKBOOK_HEAD_POLL_MS)
      // Keeps the session alive during continuous work; focus, visibility and reconnect also renew it.
      heartbeat = setInterval(() => { void recheck() }, WORKBOOK_SESSION_HEARTBEAT_MS)
      container.current.append(frame)
    })()

    return () => {
      disposed = true
      pollRef.current = () => {}
      bridgeRef.current = undefined
      detach()
      bridge?.close("closed")
    }
  }, [client])

  // `workbook.committed` on `page:{pageId}` names a confirmed revision; the bridge reads its content
  // through the commits feed (duplicates and stale revisions are no-ops). A reconnect means pushes
  // may have been missed, so read from the last revision.
  useRealtimeChannelSafe(`page:${pageId}`)
  useRealtimeEventSafe(WORKBOOK_COMMITTED_EVENT as RealtimeEventType, event => {
    const head = parseCommittedPush(event.payload, pageId)
    if (head) bridgeRef.current?.notify(head)
  })
  useRealtimeEventSafe("realtime.reconnected", () => { bridgeRef.current?.online(); pollRef.current() })

  // Keep mine writes every cell of the refused edit against the head; take theirs drops only the
  // conflicting cells and still writes the others. The bridge reports the refusal as sending, then
  // drops it on the receipt or opens it again; the status says "refused" until none is open.
  // A decision the full save queue cannot take yet is not silently dropped: the row says to try again.
  const decided = React.useCallback((id: number, done: boolean | undefined) => {
    const bridge = bridgeRef.current
    if (done) { refocus.current = true; setQueueFull(shown => shown.has(id) ? new Set([...shown].filter(item => item !== id)) : shown) }
    else if (bridge && bridge.phase === "live" && bridge.pending >= BRIDGE_MAX_PENDING) setQueueFull(shown => shown.has(id) ? shown : new Set([...shown, id]))
  }, [])
  const keepMine = React.useCallback((id: number) => decided(id, bridgeRef.current?.keepMine(id)), [decided])
  const takeTheirs = React.useCallback((id: number) => decided(id, bridgeRef.current?.takeTheirs(id)), [decided])
  const discardOverflow = React.useCallback(() => { if (bridgeRef.current?.discardOverflow()) refocus.current = true }, [])
  // Once the queue has room, the "try again" notes are stale.
  React.useEffect(() => { if (save.pending < BRIDGE_MAX_PENDING) setQueueFull(shown => shown.size ? new Set() : shown) }, [save.pending])
  // The clicked row went away or its buttons are disabled: focus moves to the next open notice
  // itself (not to a button in it, so a repeated Enter decides nothing), or the editor.
  React.useEffect(() => {
    if (!refocus.current) return
    refocus.current = false
    const next = panel.current?.querySelector<HTMLElement>("[data-kind='refused'][data-state='open'], [data-kind='overflow']")
    if (next) next.focus()
    else container.current?.querySelector("iframe")?.focus()
  }, [refusals, overflow])

  const label = workbookSaveLabel(save)
  return <section aria-label="Workbook page" className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
    <div className="grid h-24 shrink-0 grid-cols-[minmax(0,1fr)_auto] grid-rows-[1.25rem_minmax(0,1fr)] gap-x-3 border-b px-3 py-2">
      <h1 className="truncate font-semibold" title={name ?? "Workbook"}>{name ?? "Workbook"}</h1>
      {rev !== undefined && rev > 0 && <span className="text-sm tabular-nums text-muted-foreground">Revision {rev}</span>}
      <span role="status" aria-live="polite" data-testid="workbook-save-state" className="col-span-2 overflow-y-auto text-sm text-muted-foreground">{label}</span>
    </div>
    {/* Notices must not reflow the credentialless frame during a pointer gesture: Chromium can
        leave focus on the outer iframe and deliver the next keystrokes to the host. */}
    <ConflictNotice conflicts={conflicts} refusals={refusals} overflow={overflow} queueFull={queueFull} onKeepMine={keepMine} onTakeTheirs={takeTheirs} onDiscardOverflow={discardOverflow} panel={panel} />
    <div ref={container} data-testid="workbook-editor-frame" className="relative min-h-0 flex-1" />
  </section>
}
