"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { useRealtimeChannelSafe, useRealtimeEventSafe, useRealtimeStatusSafe, type RealtimeEventType } from "@/hooks/use-realtime"
import { initialSaveState, losesWork, saveReducer, type SaveState } from "@/lib/editor-lifecycle/save-state"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { EDITOR_FRAME_ATTRIBUTES, createWorkbookBridge, type FrameWindow, type KillReason, type WorkbookBridge } from "@/lib/workbooks/bridge"
import { WORKBOOK_COMMITTED_EVENT, createWorkbookEditorClient, editorFrameSrc, parseCommittedPush, type SessionCheck, type WorkbookEditorClient } from "@/lib/workbooks/editor-client"
import { PageWorkbook } from "./page-workbook"

/** How often the host reads the head revision while the editor is visible and no realtime push can arrive. */
export const WORKBOOK_HEAD_POLL_MS = 2000
/** The same read while the realtime connection is up: `workbook.committed` drives it, this is the fallback. */
export const WORKBOOK_HEAD_POLL_CONNECTED_MS = 15_000
/** Session renewal while the editor is open and visible, well inside the shortest session lifetime. */
export const WORKBOOK_SESSION_HEARTBEAT_MS = 60_000
/** New frames opened in a row after the workbook moved to a new generation. */
export const WORKBOOK_GENERATION_REOPENS = 3
/** Least time between two rechecks of the open session (focus, reconnect, back online). */
export const WORKBOOK_SESSION_RECHECK_MS = 5000

/** Why the editor is not (or no longer) showing. `closed` is an unmount and never reaches the UI. */
export type EditorEnd =
  | { reason: Exclude<KillReason, "closed">; unsaved: number }
  /** The server refused or failed to open a session (`status` 0: network or unreadable). */
  | { reason: "unavailable"; status: number }
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
  if (state.pending > 0) {
    const unsent = `${state.pending} ${state.pending === 1 ? "change" : "changes"} not saved`
    if (state.trouble === "offline") return `Offline · ${unsent}`
    if (state.trouble === "stuck") return `Not saved · server not answering · ${unsent}`
    if (state.trouble === "retrying" || state.trouble === "rate-limited") return "Saving… · retrying"
    if (state.trouble === "rejected" || state.trouble === "unconfirmed") return `Not saved · ${unsent}`
    return "Saving…"
  }
  if (state.receipt) return "Saved"
  if (state.trouble === "rejected" || state.trouble === "conflict") return "Not saved · a change was refused"
  if (state.trouble === "unconfirmed") return "Not saved · the server did not confirm a change"
  return "No unsaved changes"
}

function endMessage(end: EditorEnd): { text: string; reopen: boolean } {
  const unsaved = "unsaved" in end && end.unsaved > 0 ? ` · ${end.unsaved} ${end.unsaved === 1 ? "change" : "changes"} not saved` : ""
  switch (end.reason) {
    case "unavailable": return { text: end.status === 503 ? "The workbook editor is turned off. Showing the read-only preview." : "The workbook editor is unavailable. Showing the read-only preview.", reopen: end.status === 0 || end.status >= 500 }
    case "config": return { text: "The workbook editor cannot be opened safely in this browser. Showing the read-only preview.", reopen: false }
    case "access": return { text: `Access lost${unsaved}`, reopen: false }
    case "disabled": return { text: `The workbook editor is turned off${unsaved}. Showing the read-only preview.`, reopen: false }
    case "expired": return { text: `The editor session ended${unsaved}. Open the editor again to continue.`, reopen: true }
    case "load": return { text: `The workbook editor could not load the workbook${unsaved}. Showing the read-only preview.`, reopen: true }
    case "timeout": return { text: "The workbook editor did not start. Showing the read-only preview.", reopen: true }
    case "generation": return { text: `This workbook changed version. Open the editor again to continue${unsaved}.`, reopen: true }
    default: return { text: `The editor was closed because its frame broke the protocol${unsaved}. Showing the read-only preview.`, reopen: true }
  }
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
  const [end, setEnd] = React.useState<EditorEnd>()
  const [attempt, setAttempt] = React.useState(0)
  const reopens = React.useRef(0)

  React.useEffect(() => {
    let live = true
    setEnabled(null)
    void editorClient.editorEnabled().then(value => { if (live) setEnabled(value) })
    return () => { live = false }
  }, [editorClient])

  const onEnd = React.useCallback((next: EditorEnd) => {
    // A new generation needs a new frame and session; the old frame never sees the new content.
    if (next.reason === "generation" && reopens.current < WORKBOOK_GENERATION_REOPENS) {
      reopens.current++
      setAttempt(value => value + 1)
      return
    }
    setEnd(next)
  }, [])
  const reopen = () => { reopens.current = 0; setEnd(undefined); setAttempt(value => value + 1) }

  if (enabled === true && !end && editorFrameSupported()) {
    return <WorkbookEditorHost key={attempt} client={editorClient} pageId={pageId} name={name} onEnd={onEnd} onDirtyChange={onDirtyChange} />
  }
  const notice = end && endMessage(end)
  return <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
    {notice && <div role="alert" data-testid="workbook-editor-notice" data-reason={end.reason} className="flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm">
      <span className="mr-auto">{notice.text}</span>
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
      if (!opened.ok) { finish({ reason: "unavailable", status: opened.status }); return }
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

  const label = workbookSaveLabel(save)
  return <section aria-label="Workbook page" className="flex min-h-0 flex-1 flex-col overflow-hidden">
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b p-3">
      <h1 className="mr-auto font-semibold">{name ?? "Workbook"}</h1>
      {rev !== undefined && rev > 0 && <span className="text-sm tabular-nums text-muted-foreground">Revision {rev}</span>}
      <span role="status" aria-live="polite" data-testid="workbook-save-state" className="text-sm text-muted-foreground">{label}</span>
    </div>
    <div ref={container} data-testid="workbook-editor-frame" className="relative min-h-0 flex-1" />
  </section>
}
