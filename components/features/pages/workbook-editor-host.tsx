"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { initialSaveState, losesWork, saveReducer, type SaveState } from "@/lib/editor-lifecycle/save-state"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { EDITOR_FRAME_ATTRIBUTES, createWorkbookBridge, type FrameWindow, type KillReason, type WorkbookBridge } from "@/lib/workbooks/bridge"
import { createWorkbookEditorClient, editorFrameSrc, type WorkbookEditorClient } from "@/lib/workbooks/editor-client"
import { PageWorkbook } from "./page-workbook"

/** How often the host reads the head revision while the editor is visible (the push backstop). */
export const WORKBOOK_HEAD_POLL_MS = 2000
/** New frames opened in a row after the workbook moved to a new generation. */
export const WORKBOOK_GENERATION_REOPENS = 3

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
    if (state.trouble === "rejected") return `Not saved · ${unsent}`
    return "Saving…"
  }
  if (state.receipt) return "Saved"
  if (state.trouble === "rejected" || state.trouble === "conflict") return "Not saved · a change was refused"
  return "No unsaved changes"
}

function endMessage(end: EditorEnd): { text: string; reopen: boolean } {
  const unsaved = "unsaved" in end && end.unsaved > 0 ? ` · ${end.unsaved} ${end.unsaved === 1 ? "change" : "changes"} not saved` : ""
  switch (end.reason) {
    case "unavailable": return { text: end.status === 503 ? "The workbook editor is turned off. Showing the read-only preview." : "The workbook editor is unavailable. Showing the read-only preview.", reopen: end.status === 0 || end.status >= 500 }
    case "config": return { text: "The workbook editor cannot be opened safely in this browser. Showing the read-only preview.", reopen: false }
    case "access": return { text: `Access lost${unsaved}`, reopen: false }
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
    return <WorkbookEditorHost key={attempt} client={editorClient} name={name} onEnd={onEnd} onDirtyChange={onDirtyChange} />
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
 * (navigation, broken protocol, lost access, new generation). Unsent edits hold a leave guard.
 */
export function WorkbookEditorHost({ client, name, onEnd, onDirtyChange }: {
  client: WorkbookEditorClient
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

  useLeaveGuard(losesWork(save), onDirtyChange)

  React.useEffect(() => {
    let disposed = false
    let bridge: WorkbookBridge | undefined
    let frame: HTMLIFrameElement | null = null
    let timer: ReturnType<typeof setInterval> | undefined
    let polling = false
    let sessionGeneration = 0

    const onMessage = (event: MessageEvent) => bridge?.handleWindowMessage(event)
    const onLoad = () => bridge?.frameLoaded()
    const online = () => bridge?.online()
    const offline = () => bridge?.offline()
    const detach = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
      window.removeEventListener("message", onMessage)
      window.removeEventListener("online", online)
      window.removeEventListener("offline", offline)
      document.removeEventListener("visibilitychange", poll)
      window.removeEventListener("focus", poll)
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
      try {
        const result = await client.head(current.confirmedRev)
        if (current.phase !== "live") return
        if (result.ok) { current.notify(result.head); setRev(current.confirmedRev); return }
        if ([401, 403, 404].includes(result.status)) current.close("access")
        // Beyond the feed's window: the bridge reads the feed, gets 410 and reloads the snapshot.
        else if (result.status === 410) current.notify({ rev: current.confirmedRev + 1, generation: sessionGeneration })
      } finally {
        polling = false
      }
    }
    pollRef.current = () => { void poll() }

    void (async () => {
      const opened = await client.openSession()
      if (disposed) {
        if (opened.ok) void client.transport(opened.session).closeSession().catch(() => {})
        return
      }
      if (!opened.ok) { finish({ reason: "unavailable", status: opened.status }); return }
      const session = opened.session
      sessionGeneration = session.generation
      const transport = client.transport(session)
      const src = editorFrameSrc(session.editor_url, window.location.origin)
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
        onKill: kill => {
          // The frame goes first: after a kill no document in it may hold anything of this session.
          detach()
          if (kill.reason !== "closed") finish({ reason: kill.reason, unsaved: kill.unsaved })
        },
      })
      bridgeRef.current = bridge
      window.addEventListener("message", onMessage)
      frame = createEditorFrame(document, src, onLoad)
      if (!frame || !container.current) { bridge.close("handshake"); return }
      window.addEventListener("online", online)
      window.addEventListener("offline", offline)
      document.addEventListener("visibilitychange", poll)
      window.addEventListener("focus", poll)
      timer = setInterval(() => { void poll() }, WORKBOOK_HEAD_POLL_MS)
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

  // The workbook push (`workbook.committed`) is not on the realtime wire yet; until it is, a
  // reconnect still means pushes may have been missed, so read from the last revision.
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
