"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeChannelSafe, useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { createCapabilityChannel, decodeMessage, record, revision, transferEditorPort, verifyEditorHello, type CapabilityPort } from "@/lib/mooring/channel"
import { parseRevisionPush } from "@/lib/mooring/revision"
import { durableReceipt } from "@/lib/mooring/editor-sdk"
import { editorFrameSrc } from "@/lib/workbooks/editor-client"
import { createEditorFrame, editorFrameSupported } from "./workbook-editor-host"

/** Manifest-driven application host. App IDs never select executable host code. */
export function MooringEditorHost({ workspaceId, pageId, name, onDirtyChange }: { workspaceId: string; pageId: string; name?: string; onDirtyChange?: (value: boolean) => void }) {
  const mount = React.useRef<HTMLDivElement>(null)
  const [label, setLabel] = React.useState("Opening editor…")
  const [error, setError] = React.useState<string>()
  const [dirty, setDirty] = React.useState(false)
  const [canRetry, setCanRetry] = React.useState(false)
  const [attempt, setAttempt] = React.useState(0)
  const refresh = React.useRef<() => void>(() => {})
  useLeaveGuard(dirty)
  useRealtimeChannelSafe(`page:${pageId}`)
  useRealtimeEventSafe("mooring.committed", event => {
    if (parseRevisionPush(event.payload, pageId)) refresh.current()
  })
  useRealtimeEventSafe("realtime.reconnected", () => refresh.current())
  React.useEffect(() => { onDirtyChange?.(dirty) }, [dirty, onDirtyChange])
  React.useEffect(() => {
    setError(undefined); setCanRetry(false); setLabel("Opening editor…")
    const controller = new AbortController()
    let port: CapabilityPort | undefined, frame: HTMLIFrameElement | undefined
    let loads = 0, connected = false, dead = false, wanted = false, draft = false
    let loading: Promise<void> | undefined
    let committing = false
    let latest = -1, generation = 0, bundle = "", pending: { seq: number; body: string } | undefined
    const requestID = crypto.randomUUID()
    const base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/mooring`
    const query = new URLSearchParams({ workspace_id: workspaceId })
    const send = (message: object) => port?.postMessage(JSON.stringify({ ...message, protocol: 1 }))
    const update = () => {
      setDirty(draft || !!pending)
      setLabel(pending ? "Saving…" : draft ? "Not saved · draft" : connected ? "Saved" : "Connecting…")
    }
    const stop = (message: string, retryStartup = false) => {
      // A fresh frame is safe only before this editor has ever connected.
      // Live errors must retain its draft and pending mutation instead.
      setCanRetry(retryStartup && !connected && !draft && !pending)
      dead = true; setError(message); setLabel("Editor unavailable")
      if (port) { port.onmessage = null; port.close(); port = undefined }
      frame?.remove(); frame = undefined
    }
    const call = async (suffix: string, init: RequestInit = {}) => {
      const response = await apiFetch(`${base}${suffix}?${query}`, { ...init, signal: controller.signal, cache: "no-store", skipRefresh: true })
      if ([401, 403, 404].includes(response.status)) { stop("Access to this document is unavailable."); throw new Error("Access lost") }
      return { response, body: await response.json().catch(() => null) as unknown }
    }
    async function readSnapshot() {
      try {
        const { response, body } = await call("")
        if (dead || controller.signal.aborted) return
        if (!response.ok || !record(body) || !record(body.state) || !revision(body.state.rev) || !revision(body.state.generation) || !record(body.editor) || !("snapshot" in body.state)) throw new Error("The application document could not be loaded.")
        if (generation && generation !== body.state.generation) { stop("This document changed generation. Reopen it to continue."); return }
        generation = body.state.generation
        if (!frame) {
          const src = editorFrameSrc(typeof body.editor.url === "string" ? body.editor.url : undefined, String(body.editor.bundle_digest), window.location.origin)
          if (!src || !editorFrameSupported()) { stop("This editor cannot be opened safely. Use current Chrome or Edge, or ask an administrator to check the approved editor package."); return }
          bundle = String(body.editor.bundle_digest)
          frame = createEditorFrame(document, src, () => { loads++; if (loads > 1 || connected) stop("The editor navigated unexpectedly. Reopen it to continue.") }) ?? undefined
          if (!frame) { stop("The browser cannot isolate this editor."); return }
          frame.title = `${name ?? "Application"} editor`
          mount.current?.append(frame)
        } else if (bundle !== body.editor.bundle_digest) { stop("The approved editor version changed. Reopen it to continue."); return }
        if (body.state.rev >= latest) {
          latest = body.state.rev
          if (connected) send({ type: "snapshot", rev: latest, generation, snapshot: body.state.snapshot })
        }
        if (connected) send({ type: "online" })
        setError(undefined); update()
      } catch (cause) {
        if (!controller.signal.aborted && !dead) { setError(cause instanceof Error ? cause.message : "The document could not be loaded."); setLabel("Offline · changes are not confirmed"); send({ type: "offline" }) }
      }
    }
    function load(): Promise<void> {
      if (dead) return Promise.resolve()
      wanted = true
      if (loading) return loading
      // Every caller joins the complete drain, including a fresh read queued
      // while the first GET was in flight. A commit receipt can overtake that
      // first GET, so joining only that old response is not sufficient.
      loading = (async () => {
        try {
          while (wanted && !dead) {
            wanted = false
            await readSnapshot()
          }
        } finally { loading = undefined }
      })()
      return loading
    }
    async function commit() {
      const writing = pending
      if (!writing || dead || committing) return
      committing = true
      try {
        const { response, body } = await call("/commands", { method: "POST", headers: { "content-type": "application/json" }, body: writing.body })
        if (dead || controller.signal.aborted || pending !== writing) return
        if (!response.ok) {
          if (response.status >= 500 || response.status === 429) throw new Error("The server has not confirmed this change. Reconnecting will retry it safely.")
          pending = undefined; draft = true
          send({ type: "rejected", seq: writing.seq, message: "Change refused; your draft is preserved. Review the confirmed content and save again." })
          setError("The change was refused. Your draft is not saved."); update(); void load(); return
        }
        const receipt = record(body) && record(body.receipt) ? body.receipt : body
        if (!record(receipt) || !revision(receipt.rev) || !durableReceipt(receipt, receipt.rev, "crewship")) throw new Error("The server did not provide a durable receipt.")
        await load()
        if (dead || controller.signal.aborted || latest < receipt.rev) throw new Error("Waiting for the confirmed revision.")
        pending = undefined
        send({ type: "ack", seq: writing.seq, rev: receipt.rev, receipt })
        update()
      } catch (cause) {
        if (!controller.signal.aborted && !dead) { setError(cause instanceof Error ? cause.message : "Change not confirmed."); setLabel("Offline · change not confirmed"); send({ type: "offline" }) }
      } finally { committing = false }
    }
    const receive = (raw: unknown) => {
      const message = decodeMessage(raw)
      if (!message) { stop("The editor sent an invalid protocol message."); return }
      if (message.type === "draft" && typeof message.dirty === "boolean") { draft = message.dirty; update(); return }
      if (message.type !== "command" || !revision(message.seq) || message.seq < 1 || !revision(message.base_rev) || message.generation !== generation || typeof message.command !== "string" || !("arguments" in message) || pending) { stop("The editor sent an unsupported command."); return }
      pending = { seq: message.seq, body: JSON.stringify({ mutation_id: `${requestID}:${message.seq}`, base_rev: message.base_rev, generation, command: message.command, arguments: message.arguments }) }
      update(); void commit()
    }
    const windowMessage = (event: MessageEvent) => {
      if (dead) return
      const hello = verifyEditorHello(event, frame?.contentWindow, { namespace: "mooring", bundle, loads, waiting: !connected })
      if (hello === null) return
      if (hello !== "accept" || !frame?.contentWindow) { stop("The editor handshake did not match the approved protocol and package."); return }
      port = transferEditorPort(frame.contentWindow, createCapabilityChannel(), "mooring", {}, receive)
      connected = true; void load()
    }
    const reconnect = () => { if (pending) void commit(); else void load() }
    const offline = () => { setLabel("Offline · changes are not confirmed"); send({ type: "offline" }) }
    window.addEventListener("message", windowMessage)
    window.addEventListener("online", reconnect)
    window.addEventListener("offline", offline)
    window.addEventListener("focus", reconnect)
    refresh.current = () => void load()
    const poll = setInterval(reconnect, 15_000)
    const timeout = setTimeout(() => { if (!connected) stop("The editor did not start. Its address may be unavailable or blocked by this network. Retry, or ask an administrator to check the editor address.", true) }, 15_000)
    void load()
    return () => {
      dead = true; controller.abort(); clearInterval(poll); clearTimeout(timeout)
      refresh.current = () => {}
      window.removeEventListener("message", windowMessage); window.removeEventListener("online", reconnect); window.removeEventListener("offline", offline); window.removeEventListener("focus", reconnect)
      if (port) { port.onmessage = null; port.close() }
      frame?.remove()
    }
  }, [workspaceId, pageId, name, attempt])
  return <section aria-label="Application editor" className="flex min-h-0 flex-1 flex-col">
    <div className="flex h-12 shrink-0 items-center gap-3 border-b px-3"><h1 className="truncate font-semibold">{name ?? "Application"}</h1><span role="status" className="text-sm">{label}</span></div>
    <div className="relative min-h-0 flex-1"><div ref={mount} className="absolute inset-0" />{error && <div role="alert" className="absolute inset-x-2 top-2 z-10 rounded border bg-background p-3 text-sm">
      <p>{error}</p>
      {canRetry && <Button type="button" size="sm" variant="outline" className="mt-2" onClick={() => { setCanRetry(false); setAttempt(value => value + 1) }}>Retry editor</Button>}
    </div>}</div>
  </section>
}
